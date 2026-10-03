#!/usr/bin/env python3
"""Run local M1 integration trials; toggles sources and stops/rebuilds demo services."""
import argparse
import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def compose(*args):
    return subprocess.check_output(["docker", "compose", *args], text=True, timeout=180)


def request(url, payload=None, token=None, headers=None):
    headers = {"X-Correlation-ID": "m1-integration-trial", **(headers or {})}
    if token:
        headers["Authorization"] = "Bearer " + token
    if payload is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, json.loads(response.read()), dict(response.headers)


def eventually(probe, timeout=35):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            value = probe()
        except (urllib.error.URLError, TimeoutError, subprocess.CalledProcessError):
            value = None # Expected while a stopped service starts accepting requests.
        if value:
            return value
        time.sleep(1)
    raise RuntimeError("trial condition did not become true before deadline")


class Trials:
    def __init__(self):
        self.env = {}
        for line in Path(".env").read_text().splitlines():
            if line.strip() and not line.lstrip().startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                self.env[key.strip()] = value.strip().strip("\"'")
        self.env.update(os.environ)
        self.api = "http://127.0.0.1:" + self.env.get("API_HTTP_PORT", "8080")
        self.auth = "http://127.0.0.1:" + self.env.get("AUTH_HTTP_PORT", "8084")
        self.pvmbg = "http://127.0.0.1:" + self.env.get("PVMBG_MOCK_HTTP_PORT", "8082")
        self.results = []

    def record(self, check, **details):
        item = {"check": check, **details}
        self.results.append(item)
        print(json.dumps(item), flush=True)

    def login(self, identity="field-team"):
        key = {"field-team": "FIELD_TEAM_CLIENT_SECRET", "media": "MEDIA_CLIENT_SECRET", "bnpb-internal": "BNPB_INTERNAL_CLIENT_SECRET"}[identity]
        status, body, _ = request(self.auth + "/v1/auth/login", {"client_id": identity, "client_secret": self.env[key]})
        assert status == 200, f"login {identity}: HTTP {status}"
        return body

    def read(self, token, query="?source=BMKG&view=full&limit=50"):
        return request(self.api + "/v1/hazards" + query, token=token)

    def admin(self, path, body):
        status, result, _ = request(self.pvmbg + "/admin/" + path, body, token=self.env["PVMBG_TOKEN"])
        assert status == 200, f"source control {path}: HTTP {status}"
        return result

    def inspect(self, service):
        binary = "/dashboard-consumer" if service == "dashboard-consumer" else "/notifier-consumer"
        return json.loads(compose("exec", "-T", service, binary, "inspect"))

    def container_state(self, services=None):
        identifiers = compose("ps", "-q", *(services or [])).split()
        containers = json.loads(subprocess.check_output(["docker", "inspect", *identifiers], text=True))
        return {c["Config"]["Labels"]["com.docker.compose.service"]: {
            "id": c["Id"], "started_at": c["State"]["StartedAt"], "restart_count": c["RestartCount"]
        } for c in containers}

    def history(self, source, ref, revisions):
        return json.loads(compose("exec", "-T", "aggregator", "/aggregator", "history-audit",
                                  "--source", source, "--source-ref-id", ref, "--revisions", revisions))

    def broker_stream(self):
        broker = json.loads(compose("exec", "-T", "nats", "wget", "-qO-", "http://127.0.0.1:8222/jsz?streams=true&consumers=true"))
        return next(stream for account in broker.get("account_details", []) for stream in account.get("stream_detail", [])
                    if stream["name"] == self.env.get("NATS_STREAM_NAME", "HAZARD_EVENTS"))

    def broker_consumers(self):
        return {consumer["name"]: consumer for consumer in self.broker_stream().get("consumer_detail", [])}

    def remove_trial_consumer(self, durable):
        assert durable.startswith("third-consumer-trial-"), "cleanup may only remove trial durables"
        if durable not in self.broker_consumers():
            return
        reply = "_INBOX." + durable
        connect = json.dumps({"auth_token": self.env["NATS_TOKEN"], "verbose": False, "pedantic": False})
        subject = "$JS.API.CONSUMER.DELETE." + self.env.get("NATS_STREAM_NAME", "HAZARD_EVENTS") + "." + durable
        # One local NATS API request via nc; credentials travel through stdin only.
        frames = f"CONNECT {connect}\r\nSUB {reply} 1\r\nPUB {subject} {reply} 0\r\n\r\nPING\r\n".encode()
        response = subprocess.check_output(["docker", "compose", "exec", "-T", "nats", "nc", "-w", "2", "127.0.0.1", "4222"], input=frames, timeout=10)
        assert any(json.loads(line).get("success") is True for line in response.splitlines() if line.startswith(b"{")), "temporary durable cleanup failed"

    def smoke(self):
        identities = ["media", "field-team", "bnpb-internal"]
        secrets = [self.env[key] for key in ["MEDIA_CLIENT_SECRET", "FIELD_TEAM_CLIENT_SECRET", "BNPB_INTERNAL_CLIENT_SECRET"]]
        assert len(set(secrets)) == 3 and all(secrets), "client secrets must be nonempty and distinct"
        summary = {"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"}
        for identity in identities:
            token = self.login(identity)["access_token"]
            view = "summary" if identity == "media" else "full"
            status, body, headers = self.read(token, f"?source=BMKG&view={view}&limit=50")
            assert status == 200 and body["data"]
            assert headers["X-Correlation-Id"] == body["meta"]["correlation_id"] == "m1-integration-trial"
            if identity == "media":
                assert all(set(row) == summary for row in body["data"])
                assert self.read(token)[0] == 403
            else:
                assert "attributes" in body["data"][0]
            self.record("client access", identity=identity, status=status, view=view, fields=len(body["data"][0]))
        assert request(self.api + "/v1/hazards")[0] == 401
        bmkg = "http://127.0.0.1:" + self.env.get("BMKG_MOCK_HTTP_PORT", "8081")
        assert request(bmkg + "/seismic-events", headers={"X-BMKG-Key": self.env["PVMBG_TOKEN"]})[0] in (401, 403)
        assert request(self.pvmbg + "/volcanic-reports", token=self.env["BMKG_API_KEY"])[0] in (401, 403)
        self.record("cross-source credentials rejected", unauthenticated_api_status=401)
        token = self.login()["access_token"]
        try:
            self.admin("outage", {"enabled": True})
            def stale():
                status, body, _ = self.read(token, "?source=PVMBG&view=full&limit=50")
                assert status != 500
                if status == 200 and body["meta"]["sources"]["PVMBG"]["status"] == "stale":
                    return {"status": status, "freshness": "stale", "records": len(body["data"])}
            self.record("live PVMBG outage", **eventually(stale))
            assert self.read(token)[0] == 200
        finally:
            self.admin("outage", {"enabled": False})
        eventually(lambda: self.read(token, "?source=PVMBG&view=full&limit=50")[1]["meta"]["sources"]["PVMBG"]["status"] == "fresh")
        before = self.container_state()
        try:
            self.admin("schema-version", {"version": 1})
            status, baseline, _ = request(self.pvmbg + "/volcanic-reports", token=self.env["PVMBG_TOKEN"])
            assert status == 200 and all("confidence_level" not in r for r in baseline)
            known = {r["report_id"] for r in baseline}
            # Select a newly generated v1 record so revisions 1/2 also work on repeated trials.
            def new_report():
                status, reports, _ = request(self.pvmbg + "/volcanic-reports", token=self.env["PVMBG_TOKEN"])
                return next((r for r in reports if r["report_id"] not in known and "confidence_level" not in r), None) if status == 200 else None
            raw = eventually(new_report)
            ref = raw["report_id"]
            def v1_history():
                audit = self.history("PVMBG", ref, "1")
                return audit if "confidence_level" not in audit["versions"][0]["data"]["attributes"] else None
            v1 = eventually(v1_history)
            canonical = v1["versions"][0]["data"]
            assert canonical["source_ref_id"] == ref and canonical["severity"] == raw["alert_level"].upper()
            assert canonical["occurred_at"] == raw["reported_at"]
            for field in ["volcano_id", "eruption_count_24h", "ash_column_height_m"]:
                assert canonical["attributes"][field] == raw[field]
            self.record("PVMBG v1 mapping", source_record=raw, canonical_record=canonical)
            self.admin("schema-version", {"version": 2})
            def both_versions():
                audit = self.history("PVMBG", ref, "1,2")
                return audit if "confidence_level" in audit["versions"][1]["data"]["attributes"] else None
            v2 = eventually(both_versions)
            assert v2["hazard_id"] == v1["hazard_id"]
            assert v2["migrations"] and v2["migrations"] == v1["migrations"]
            assert self.container_state(list(before)) == before
            self.record("schema v1/v2 coexist without migration or service restart", audit=v2,
                        before=before, after=self.container_state(list(before)))
        finally:
            self.admin("schema-version", {"version": int(self.env.get("PVMBG_SCHEMA_VERSION", "1"))})
        status, raw_events, _ = request(bmkg + "/seismic-events", headers={"X-BMKG-Key": self.env["BMKG_API_KEY"]})
        assert status == 200 and len(raw_events) >= 20
        raw = next(r for r in raw_events if not r["potential_tsunami"])
        mapped = eventually(lambda: self.history("BMKG", raw["event_id"], "1"))["versions"][0]["data"]
        expected = "NORMAL" if raw["magnitude"] < 5 else "WASPADA" if raw["magnitude"] < 6.5 else "SIAGA"
        assert mapped["severity"] == expected and mapped["source_ref_id"] == raw["event_id"]
        for canonical_field, source_field in [("area_name", "region_name"), ("latitude", "epicenter_lat"), ("longitude", "epicenter_lon"), ("occurred_at", "occurred_at")]:
            assert mapped[canonical_field] == raw[source_field]
        for field in ["magnitude", "depth_km", "potential_tsunami"]:
            assert mapped["attributes"][field] == raw[field]
        self.record("BMKG mapping", source_record=raw, canonical_record=mapped, seed_records=len(raw_events))
        for service in ["dashboard-consumer", "notifier-consumer"]:
            result = self.inspect(service)
            assert result["inbox_count"] > 0
            self.record("events reach consumer", consumer=service, inbox_count=result["inbox_count"], quarantine_count=result["quarantine_count"])

    def tokens(self):
        session = self.login()
        assert session["expires_in"] == 60, "expiry trial requires the M1 default TTL of 60 seconds"
        token = session["access_token"]
        encoded = token.split(".")[1]
        claims = json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4)))
        try:
            compose("stop", "auth")
            assert self.read(token)[0] == 200
            self.record("valid JWT during Auth outage", status=200)
        finally:
            compose("start", "auth")
        eventually(lambda: request(self.auth + "/ready")[0] == 200)
        wait = max(0, claims["exp"] - time.time() + 0.3)
        self.record("waiting for natural expiry", seconds=round(wait, 2), ttl_seconds=60)
        time.sleep(wait)
        assert self.read(token)[0] == 401
        status, rotated, _ = request(self.auth + "/v1/auth/refresh", {"refresh_token": session["refresh_token"]})
        assert status == 200 and self.read(rotated["access_token"])[0] == 200
        assert self.read(token)[0] == 401
        self.record("expiry and persistent refresh after restart", old_access_status=401, new_access_status=200)
        assert request(self.auth + "/v1/auth/refresh", {"refresh_token": session["refresh_token"]})[0] == 401
        assert request(self.auth + "/v1/auth/refresh", {"refresh_token": rotated["refresh_token"]})[0] == 401
        self.record("refresh replay revokes family", status=401)

    def events(self):
        token = self.login()["access_token"]
        before = self.inspect("notifier-consumer")["inbox_count"]
        producer = self.container_state(["aggregator"])
        stream_before = self.broker_stream()["state"]["last_seq"]
        started = time.monotonic()
        try:
            compose("stop", "dashboard-consumer")
            statuses = []
            for _ in range(3):
                statuses.append(self.read(token)[0])
                time.sleep(5)
            assert all(status == 200 for status in statuses)
            target = eventually(lambda: (count if (count := self.inspect("notifier-consumer")["inbox_count"]) > before else None))
            stream_after = self.broker_stream()["state"]["last_seq"]
            assert stream_after > stream_before, "producer published no new events during downtime"
            checkpoint = eventually(lambda: (seq if (seq := self.broker_consumers()[self.env.get("NATS_NOTIFIER_DURABLE", "notifier-v1")]["ack_floor"]["stream_seq"]) >= stream_after else None))
            self.record("one consumer down", stream_before=stream_before, stream_after=stream_after, notifier_before=before, notifier_after=target, api_statuses=statuses, stopped_seconds=round(time.monotonic() - started, 2), target_stream_sequence=checkpoint)
        finally:
            compose("start", "dashboard-consumer")
        after = eventually(lambda: (count if (count := self.inspect("dashboard-consumer")["inbox_count"]) >= target else None), timeout=60)
        acknowledged = eventually(lambda: (seq if (seq := self.broker_consumers()[self.env.get("NATS_DASHBOARD_DURABLE", "dashboard-v1")]["ack_floor"]["stream_seq"]) >= checkpoint else None))
        self.record("dashboard catches up to stopped-period target", target=target, dashboard_after=after, target_stream_sequence=checkpoint, dashboard_ack_floor=acknowledged)
        unchanged = self.container_state()
        unchanged.pop("dashboard-consumer")
        old = compose("ps", "-q", "dashboard-consumer").strip()
        token = self.login()["access_token"]
        build = subprocess.Popen(["docker", "compose", "up", "-d", "--build", "--no-deps", "--force-recreate", "dashboard-consumer"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        statuses = []
        while build.poll() is None:
            statuses.append(self.read(token)[0])
            time.sleep(0.2)
        assert build.returncode == 0 and statuses and all(status == 200 for status in statuses)
        assert compose("ps", "-q", "dashboard-consumer").strip() != old
        assert self.container_state(list(unchanged)) == unchanged
        self.record("consumer rebuild while API remains available", api_requests=len(statuses), api_statuses=sorted(set(statuses)), other_containers_before=unchanged, other_containers_after=self.container_state(list(unchanged)))
        # A new durable proves subscription addition even if a prior demo durable exists.
        durable = "third-consumer-trial-" + str(time.time_ns())
        checkpoint = self.broker_consumers()[self.env.get("NATS_NOTIFIER_DURABLE", "notifier-v1")]["ack_floor"]["stream_seq"]
        try:
            with tempfile.NamedTemporaryFile(mode="w", suffix=".yaml") as override:
                override.write("services:\n  third-consumer:\n    environment:\n      NATS_NOTIFIER_DURABLE: " + durable + "\n")
                override.flush()
                compose("-f", "docker-compose.yml", "-f", override.name, "--profile", "demo", "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "third-consumer")
            acknowledged = eventually(lambda: (seq if (seq := self.broker_consumers()[durable]["ack_floor"]["stream_seq"]) >= checkpoint else None), timeout=60)
            third = self.inspect("third-consumer")
            assert third["inbox_count"] > 0
            assert self.container_state(["aggregator"]) == producer
            self.record("new third durable without producer changes", durable=durable, inbox_count=third["inbox_count"], target_stream_sequence=checkpoint, third_ack_floor=acknowledged, producer_container_unchanged=True)
        finally:
            compose("--profile", "demo", "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "third-consumer")
            self.remove_trial_consumer(durable)
        self.record("temporary durable removed and default third consumer restored")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=["smoke", "tokens", "events", "all"], default="smoke")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    trials = Trials()
    evidence = {"started_at": datetime.now(timezone.utc).isoformat(), "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(), "scenario": args.scenario, "results": trials.results}
    try:
        for scenario in (["smoke", "tokens", "events"] if args.scenario == "all" else [args.scenario]):
            getattr(trials, scenario)()
        evidence["passed"] = True
    except Exception as error:
        evidence.update(passed=False, failure=type(error).__name__ + ": " + str(error))
        raise
    finally:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
