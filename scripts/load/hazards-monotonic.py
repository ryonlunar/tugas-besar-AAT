#!/usr/bin/env python3
"""P2 fallback using monotonic end-to-end HTTP timings when wall clocks shift."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import http.client
import json
import math
import os
from pathlib import Path
import threading
import time
from urllib.parse import urlsplit


def connection(url):
    parsed = urlsplit(url)
    if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.query or parsed.fragment:
        raise ValueError("URL must be HTTP(S) without credentials, query or fragment")
    factory = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    return factory(parsed.hostname, parsed.port, timeout=5), parsed.path.rstrip("/")


def request(conn, method, path, payload=None, token=None):
    headers = {"X-Correlation-ID": "m1-monotonic-load"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if payload is not None:
        headers["Content-Type"] = "application/json"
    started = time.perf_counter_ns()
    conn.request(method, path, body=None if payload is None else json.dumps(payload), headers=headers)
    response = conn.getresponse()
    body = response.read()
    duration = (time.perf_counter_ns() - started) / 1e6
    assert duration >= 0, "monotonic HTTP timer went backwards"
    return response.status, body, response.getheader("Retry-After"), duration


def session(conn, base, secret, previous, stats):
    path = "/v1/auth/refresh" if previous else "/v1/auth/login"
    payload = {"refresh_token": previous["refresh_token"]} if previous else {"client_id": "field-team", "client_secret": secret}
    for attempt in range(5):
        stats["attempts"] += 1
        status, body, retry, _ = request(conn, "POST", base + path, payload)
        if status == 200:
            result = json.loads(body)
            if not result.get("access_token") or not result.get("refresh_token") or result.get("expires_in", 0) <= 0:
                raise RuntimeError("Auth returned an invalid session")
            result["refresh_at"] = time.perf_counter() + max(0, result["expires_in"] - 10)
            return result
        if status != 429:
            raise RuntimeError(f"Auth HTTP {status}")
        stats["controlled_429"] += 1
        if attempt < 4:
            try:
                wait = min(5, max(0.1 * (attempt + 1), float(retry or 0)))
            except ValueError:
                wait = 0.1 * (attempt + 1)
            time.sleep(wait)
    raise RuntimeError("Auth retries exhausted")


def percentiles(values):
    ordered = sorted(values)
    if not ordered:
        return None
    return {"min": ordered[0], "max": ordered[-1], **{f"p{p}": ordered[math.ceil(len(ordered) * p / 100) - 1] for p in (50, 95, 99)}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seconds", type=float, default=90)
    parser.add_argument("--vus-per-source", type=int, default=30)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.seconds < 60 or args.vus_per_source < 25:
        parser.error("P2 requires at least 60 seconds and 25 workers per source")
    secret = os.environ["FIELD_TEAM_CLIENT_SECRET"]
    if not secret:
        raise ValueError("FIELD_TEAM_CLIENT_SECRET must be nonempty")
    api = os.environ.get("API_URL", "http://127.0.0.1:8080")
    auth = os.environ.get("AUTH_URL", "http://127.0.0.1:8084")
    bootstrap = {"attempts": 0, "controlled_429": 0}
    auth_conn, auth_base = connection(auth)
    try:
        sessions = [session(auth_conn, auth_base, secret, None, bootstrap) for _ in range(2 * args.vus_per_source)]
    finally:
        auth_conn.close()
    if any(current["expires_in"] != 60 for current in sessions):
        raise ValueError("M1 profile requires the default 60-second access TTL")
    barrier = threading.Barrier(len(sessions) + 1)
    deadline = 0

    def worker(source, current):
        result = {"source": source, "attempts": 0, "successes": 0, "controlled_429": 0, "unexpected_errors": 0, "durations": [], "success_durations": [], "auth": {"attempts": 0, "controlled_429": 0, "failures": 0}}
        client, base = connection(api)
        refresh, refresh_base = connection(auth)
        try:
            barrier.wait(timeout=30)
            while time.perf_counter() < deadline:
                if time.perf_counter() >= current["refresh_at"]:
                    try:
                        current = session(refresh, refresh_base, secret, current, result["auth"])
                    except (OSError, http.client.HTTPException, ValueError, RuntimeError):
                        result["auth"]["failures"] += 1
                        break
                result["attempts"] += 1
                started = time.perf_counter_ns()
                try:
                    status, _, _, duration = request(client, "GET", base + f"/v1/hazards?source={source}&view=full&limit=50", token=current["access_token"])
                except (OSError, http.client.HTTPException):
                    client.close()
                    status, duration = 0, (time.perf_counter_ns() - started) / 1e6
                result["durations"].append(duration)
                if 200 <= status < 300:
                    result["successes"] += 1
                    result["success_durations"].append(duration)
                elif status == 429:
                    result["controlled_429"] += 1
                else:
                    result["unexpected_errors"] += 1
        finally:
            client.close()
            refresh.close()
        return result

    started_at = datetime.now(timezone.utc).isoformat()
    with ThreadPoolExecutor(max_workers=len(sessions)) as pool:
        futures = [pool.submit(worker, "BMKG" if index < args.vus_per_source else "PVMBG", current) for index, current in enumerate(sessions)]
        started = time.perf_counter()
        deadline = started + args.seconds
        barrier.wait(timeout=30)
        results = [future.result() for future in futures]
    elapsed = time.perf_counter() - started
    sources = {}
    for source in ("BMKG", "PVMBG"):
        selected = [r for r in results if r["source"] == source]
        sources[source] = {key: sum(r[key] for r in selected) for key in ("attempts", "successes", "controlled_429", "unexpected_errors")}
        sources[source]["all_duration_ms"] = percentiles([d for r in selected for d in r["durations"]])
        sources[source]["success_duration_ms"] = percentiles([d for r in selected for d in r["success_durations"]])
    attempts = sum(s["attempts"] for s in sources.values())
    successes = sum(s["successes"] for s in sources.values())
    errors = sum(s["unexpected_errors"] for s in sources.values())
    controlled = sum(s["controlled_429"] for s in sources.values())
    assert attempts == successes + errors + controlled
    auth_stats = {key: sum(r["auth"][key] for r in results) + bootstrap.get(key, 0) for key in ("attempts", "controlled_429", "failures")}
    error_rate = errors / max(1, attempts - controlled)
    passed = auth_stats["failures"] == 0 and error_rate < 0.01 and all(s["successes"] > 0 for s in sources.values()) and sources["BMKG"]["success_duration_ms"]["p95"] < 300
    evidence = {"started_at": started_at, "completed_at": datetime.now(timezone.utc).isoformat(), "timer": "time.perf_counter_ns; monotonic request start through complete body read, including connection setup", "percentiles": "nearest rank", "bootstrap": "one independent session per worker, obtained before the measured interval", "profile": {"vus_per_source": args.vus_per_source, "duration_seconds": args.seconds, "view": "full", "limit": 50, "identity": "field-team", "access_ttl_seconds": 60}, "elapsed_seconds": elapsed, "sources": sources, "auth": auth_stats, "hazard_unexpected_error_percent": 100 * error_rate, "hazard_successes_per_second": successes / elapsed, "passed": passed}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence), flush=True)
    if not passed:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
