#!/usr/bin/env python3
"""Read-only probe for the IF4031 M2 Satellite Feed (satellite.naufarrel.dev).

Stdlib only. Never prints the bearer token. Used once to establish the facts
that satellite/event.go, satellite/adapter.go and D4/D7/D8 depend on:
  - shape of `data` per event type (detection/update/retract/heartbeat)
  - whether `from` is inclusive/exclusive, whether heartbeats carry `id`
  - error body shape for 400/401/429
  - lateness distribution per satellite (for choosing SATELLITE_ALLOWED_LATENESS)

Usage:
  python3 scripts/m2/probe-satellite.py --read-secret .secrets/satellite-secret.yaml
  export SATELLITE_TOKEN="$(python3 scripts/m2/probe-satellite.py --read-secret .secrets/satellite-secret.yaml)"
  python3 scripts/m2/probe-satellite.py --session
  python3 scripts/m2/probe-satellite.py --stream --from 0 --seconds 60
  python3 scripts/m2/probe-satellite.py --boundary --from 10
  python3 scripts/m2/probe-satellite.py --errors
"""
import argparse
import base64
import http.client
import json
import os
import socket
import ssl
import sys
import time
from pathlib import Path
from statistics import median

HOST = "satellite.naufarrel.dev"
OUT_DIR = Path(".state/m2/satellite-probe")


def read_secret(path: str) -> str:
    """Extract the single token value from a Kubernetes Secret manifest.

    Supports `stringData: token: "..."` (plain) and `data: token: <base64>`.
    Prints ONLY the token to stdout; never prints the key name or other data.
    """
    text = Path(path).read_text()
    in_string_data = False
    in_data = False
    for raw_line in text.splitlines():
        line = raw_line.rstrip()
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip(" "))
        stripped = line.strip()
        if indent == 0:
            if stripped.startswith("stringData:"):
                in_string_data, in_data = True, False
                continue
            if stripped.startswith("data:"):
                in_data, in_string_data = True, False
                continue
            in_string_data = in_data = False
            continue
        if indent != 2:
            continue
        if ":" not in stripped:
            continue
        key, _, value = stripped.partition(":")
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if in_string_data:
            return value
        if in_data:
            return base64.b64decode(value).decode("utf-8").strip()
    raise SystemExit("no token found under stringData/data in " + path)


def _conn(timeout: float = 10.0) -> http.client.HTTPSConnection:
    ctx = ssl.create_default_context()
    return http.client.HTTPSConnection(HOST, 443, timeout=timeout, context=ctx)


def _token() -> str:
    tok = os.environ.get("SATELLITE_TOKEN", "").strip()
    if not tok:
        raise SystemExit("SATELLITE_TOKEN is not set (export it from --read-secret first)")
    return tok


def do_session() -> dict:
    conn = _conn()
    conn.putrequest("GET", "/v1/session")
    conn.putheader("Authorization", "Bearer " + _token())
    conn.endheaders()
    resp = conn.getresponse()
    body = resp.read()
    conn.close()
    result = {"status": resp.status, "body": json.loads(body.decode("utf-8", "replace"))}
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / "session.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result, indent=2))
    return result


def _read_sse_raw(path: str, seconds: float, extra_headers: dict | None = None) -> bytes:
    conn = _conn(timeout=seconds + 10)
    conn.putrequest("GET", path)
    conn.putheader("Authorization", "Bearer " + _token())
    conn.putheader("Accept", "text/event-stream")
    conn.putheader("Cache-Control", "no-cache")
    for k, v in (extra_headers or {}).items():
        conn.putheader(k, v)
    conn.endheaders()
    resp = conn.getresponse()
    if resp.status != 200:
        body = resp.read()
        conn.close()
        return b"__STATUS__" + str(resp.status).encode() + b"\n" + body
    sock = conn.sock
    sock.settimeout(1.0)
    buf = bytearray()
    deadline = time.time() + seconds
    while time.time() < deadline:
        try:
            chunk = resp.read(65536)
        except (socket.timeout, TimeoutError):
            continue
        except Exception:
            break
        if not chunk:
            break
        buf.extend(chunk)
    conn.close()
    return bytes(buf)


def _parse_sse_events(raw: bytes) -> list[dict]:
    """Minimal SSE event splitter for observation only (not the real parser)."""
    events = []
    current = {"id": None, "event": None, "data_lines": [], "raw_start": None}
    offset = 0
    lines = raw.split(b"\n")
    pos = 0
    block_start = pos
    for line in lines:
        decoded = line.decode("utf-8", "replace")
        if decoded == "" or decoded == "\r":
            if current["data_lines"] or current["event"] or current["id"] is not None:
                current["data"] = "\n".join(current["data_lines"])
                current["raw"] = raw[block_start:pos + len(line) + 1].decode("utf-8", "replace")
                events.append(current)
            current = {"id": None, "event": None, "data_lines": [], "raw_start": None}
            block_start = pos + len(line) + 1
        elif decoded.startswith(":"):
            pass
        elif decoded.startswith("id:"):
            current["id"] = decoded[3:].strip()
        elif decoded.startswith("event:"):
            current["event"] = decoded[6:].strip()
        elif decoded.startswith("data:"):
            current["data_lines"].append(decoded[5:].strip())
        pos += len(line) + 1
    return events


def _summarize_events(events: list[dict]) -> dict:
    by_type: dict[str, dict] = {}
    for ev in events:
        etype = ev["event"] or "message"
        slot = by_type.setdefault(etype, {"count": 0, "has_id": 0, "field_types": {}, "sample": None})
        slot["count"] += 1
        if ev["id"] is not None:
            slot["has_id"] += 1
        try:
            payload = json.loads(ev["data"]) if ev["data"] else None
        except json.JSONDecodeError:
            payload = None
        if isinstance(payload, dict):
            for k, v in payload.items():
                slot["field_types"].setdefault(k, type(v).__name__)
            if slot["sample"] is None:
                slot["sample"] = payload
    return by_type


def do_stream(from_offset: str, seconds: float) -> None:
    raw = _read_sse_raw(f"/v1/stream?from={from_offset}", seconds)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    raw_path = OUT_DIR / f"stream-from-{from_offset}.raw"
    raw_path.write_bytes(raw)
    events = _parse_sse_events(raw)
    summary = _summarize_events(events)
    ids = [int(e["id"]) for e in events if e["id"] is not None and e["id"].lstrip("-").isdigit()]
    gaps = [b - a for a, b in zip(ids, ids[1:]) if b - a != 1] if len(ids) > 1 else []
    report = {
        "from": from_offset,
        "seconds": seconds,
        "raw_bytes": len(raw),
        "event_count": len(events),
        "first_id": ids[0] if ids else None,
        "last_id": ids[-1] if ids else None,
        "non_contiguous_gaps": gaps[:20],
        "by_type": {
            k: {"count": v["count"], "has_id": v["has_id"], "field_types": v["field_types"], "sample": v["sample"]}
            for k, v in summary.items()
        },
    }
    (OUT_DIR / f"stream-from-{from_offset}-summary.json").write_text(json.dumps(report, indent=2, default=str))
    print(json.dumps(report, indent=2, default=str))
    lateness_by_sat: dict[str, list[float]] = {}
    for ev in events:
        if ev["event"] != "detection":
            continue
        try:
            payload = json.loads(ev["data"])
        except json.JSONDecodeError:
            continue
        sat = payload.get("satellite")
        acq_date, acq_time = payload.get("acq_date"), payload.get("acq_time")
        if not (sat and acq_date and acq_time):
            continue
        try:
            t = acq_time if len(str(acq_time)) == 4 else str(acq_time).zfill(4)
            detected_at = time.mktime(time.strptime(f"{acq_date} {t}", "%Y-%m-%d %H%M")) - time.timezone
        except Exception:
            continue
        lateness_by_sat.setdefault(sat, []).append(detected_at)
    lateness_report = {}
    for sat, times_ in lateness_by_sat.items():
        watermark = 0.0
        deltas = []
        for t in times_:
            if t < watermark:
                deltas.append(watermark - t)
            watermark = max(watermark, t)
        if deltas:
            deltas.sort()
            lateness_report[sat] = {
                "n_late": len(deltas),
                "n_total": len(times_),
                "p50_seconds": median(deltas),
                "p95_seconds": deltas[int(0.95 * (len(deltas) - 1))],
                "max_seconds": max(deltas),
            }
    (OUT_DIR / f"lateness-from-{from_offset}.json").write_text(json.dumps(lateness_report, indent=2))
    print("lateness-by-satellite:", json.dumps(lateness_report, indent=2))


def do_boundary(from_offset: str) -> None:
    raw_from = _read_sse_raw(f"/v1/stream?from={from_offset}", 5)
    ev_from = _parse_sse_events(raw_from)
    first_from_id = next((e["id"] for e in ev_from if e["id"] is not None), None)
    raw_leh = _read_sse_raw("/v1/stream", 5, extra_headers={"Last-Event-ID": str(from_offset)})
    ev_leh = _parse_sse_events(raw_leh)
    first_leh_id = next((e["id"] for e in ev_leh if e["id"] is not None), None)
    report = {
        "requested_offset": from_offset,
        "first_id_with_from_param": first_from_id,
        "first_id_with_last_event_id_header": first_leh_id,
        "interpretation": (
            "from is INCLUSIVE (resume re-delivers offset N)"
            if first_from_id == from_offset
            else "from is EXCLUSIVE (first delivered id > N)"
            if first_from_id is not None
            else "inconclusive: no event observed in 5s window"
        ),
    }
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / "boundary.json").write_text(json.dumps(report, indent=2, default=str))
    print(json.dumps(report, indent=2, default=str))


def do_errors() -> None:
    results = {}
    # 1. bad token -> expect 401
    conn = _conn()
    conn.putrequest("GET", "/v1/session")
    conn.putheader("Authorization", "Bearer definitely-not-a-valid-token")
    conn.endheaders()
    resp = conn.getresponse()
    results["bad_token"] = {"status": resp.status, "body": json.loads(resp.read().decode("utf-8", "replace"))}
    conn.close()
    # 2. invalid from value -> expect 400
    conn = _conn()
    conn.putrequest("GET", "/v1/stream?from=abc")
    conn.putheader("Authorization", "Bearer " + _token())
    conn.putheader("Accept", "text/event-stream")
    conn.endheaders()
    resp = conn.getresponse()
    results["invalid_from"] = {"status": resp.status, "body": resp.read(2048).decode("utf-8", "replace")}
    conn.close()
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / "errors.json").write_text(json.dumps(results, indent=2, default=str))
    print(json.dumps(results, indent=2, default=str))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--read-secret")
    ap.add_argument("--session", action="store_true")
    ap.add_argument("--stream", action="store_true")
    ap.add_argument("--boundary", action="store_true")
    ap.add_argument("--errors", action="store_true")
    ap.add_argument("--from", dest="from_offset", default="0")
    ap.add_argument("--seconds", type=float, default=60.0)
    args = ap.parse_args()

    if args.read_secret:
        print(read_secret(args.read_secret))
        return
    if args.session:
        do_session()
        return
    if args.stream:
        do_stream(args.from_offset, args.seconds)
        return
    if args.boundary:
        do_boundary(args.from_offset)
        return
    if args.errors:
        do_errors()
        return
    ap.error("choose one of --read-secret/--session/--stream/--boundary/--errors")


if __name__ == "__main__":
    main()
