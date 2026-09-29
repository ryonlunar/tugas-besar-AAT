#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/aat-source-smoke.XXXXXX")"
bmkg_pid=""
pvmbg_pid=""
cleanup() {
  [[ -z "$bmkg_pid" ]] || kill "$bmkg_pid" 2>/dev/null || true
  [[ -z "$pvmbg_pid" ]] || kill "$pvmbg_pid" 2>/dev/null || true
  [[ -z "$bmkg_pid" ]] || wait "$bmkg_pid" 2>/dev/null || true
  [[ -z "$pvmbg_pid" ]] || wait "$pvmbg_pid" 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}
bmkg_port="$(free_port)"
pvmbg_port="$(free_port)"
while [[ "$bmkg_port" == "$pvmbg_port" ]]; do pvmbg_port="$(free_port)"; done

go build -o "$tmpdir/bmkg-mock" ./services/bmkg-mock/cmd/server
go build -o "$tmpdir/pvmbg-mock" ./services/pvmbg-mock/cmd/server
BMKG_API_KEY='local-demo-key' BMKG_MOCK_HTTP_PORT="$bmkg_port" "$tmpdir/bmkg-mock" >"$tmpdir/bmkg.log" 2>&1 &
bmkg_pid=$!
PVMBG_TOKEN='local-demo-token' PVMBG_MOCK_HTTP_PORT="$pvmbg_port" "$tmpdir/pvmbg-mock" >"$tmpdir/pvmbg.log" 2>&1 &
pvmbg_pid=$!

ready=false
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$bmkg_port/health" >/dev/null 2>&1 && curl -fsS "http://127.0.0.1:$pvmbg_port/health" >/dev/null 2>&1; then ready=true; break; fi
  sleep 0.1
done
[[ "$ready" == true ]] || { printf 'mock services did not become healthy\n'; exit 1; }

bmkg_count="$(curl -fsS -H 'X-BMKG-Key: local-demo-key' "http://127.0.0.1:$bmkg_port/seismic-events" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
bmkg_unauth="$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$bmkg_port/seismic-events")"
[[ "$bmkg_count" == 20 && "$bmkg_unauth" == 401 ]] || { printf 'BMKG check failed: count=%s unauth=%s\n' "$bmkg_count" "$bmkg_unauth"; exit 1; }

pvmbg_auth='Authorization: Bearer local-demo-token'
pvmbg_v1="$(curl -fsS -H "$pvmbg_auth" "http://127.0.0.1:$pvmbg_port/volcanic-reports" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(str(len(d))+":"+str("confidence_level" in d[0])+":"+d[0]["report_id"])')"
curl -fsS -X POST -H "$pvmbg_auth" -H 'Content-Type: application/json' -d '{"version":2}' "http://127.0.0.1:$pvmbg_port/admin/schema-version" >/dev/null
curl -fsS -X POST -H "$pvmbg_auth" -H 'Content-Type: application/json' -d '{"min_ms":0,"max_ms":0}' "http://127.0.0.1:$pvmbg_port/admin/delay" >/dev/null
pvmbg_v2="$(curl -fsS -H "$pvmbg_auth" "http://127.0.0.1:$pvmbg_port/volcanic-reports" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(str(len(d))+":"+str("confidence_level" in d[0])+":"+d[0]["report_id"])')"
curl -fsS -X POST -H "$pvmbg_auth" -H 'Content-Type: application/json' -d '{"enabled":true}' "http://127.0.0.1:$pvmbg_port/admin/outage" >/dev/null
pvmbg_outage="$(curl -sS -o /dev/null -w '%{http_code}' -H "$pvmbg_auth" "http://127.0.0.1:$pvmbg_port/volcanic-reports")"
curl -fsS -X POST -H "$pvmbg_auth" -H 'Content-Type: application/json' -d '{"enabled":false}' "http://127.0.0.1:$pvmbg_port/admin/outage" >/dev/null
[[ "$pvmbg_v1" == 20:False:* && "$pvmbg_v2" == 20:True:* && "${pvmbg_v1##*:}" == "${pvmbg_v2##*:}" && "$pvmbg_outage" == 503 ]] || { printf 'PVMBG check failed: v1=%s v2=%s outage=%s\n' "$pvmbg_v1" "$pvmbg_v2" "$pvmbg_outage"; exit 1; }

printf 'PASS: BMKG seeded=%s unauth=%s; PVMBG v1=%s v2=%s outage=%s; schema toggle kept report ID stable\n' "$bmkg_count" "$bmkg_unauth" "$pvmbg_v1" "$pvmbg_v2" "$pvmbg_outage"
