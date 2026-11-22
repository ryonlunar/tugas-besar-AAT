#!/usr/bin/env bash
# P3.6c: accept Field Reports while processing is stopped, kill every
# Aggregator pod after the 202, resume, and confirm each report is processed
# exactly once. Run inside aat-dev from ~/aat.
set -uo pipefail
export KUBECONFIG=~/.kube/aat-dev.yaml
n=(kubectl --context aat-dev -n sistem-informasi-kebencanaan-bnpb)
svc() { "${n[@]}" get svc "$1" -o jsonpath='{.spec.clusterIP}:{.spec.ports[0].port}'; }
AUTH="http://$(svc auth)"; API="http://$(svc client-api)"
secret() { python3 -c "import json;print(json.load(open('.state/m2/credentials.json'))['$1'])"; }
login() { curl -sS -X POST "$AUTH/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"field-team\",\"client_secret\":\"$(secret FIELD_TEAM_CLIENT_SECRET)\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])'; }
settle() {  # wait until exactly 3 aggregator pods exist and none is terminating
  for _ in $(seq 1 90); do
    s=$("${n[@]}" get pods -l app=aggregator -o json | python3 -c 'import json,sys;p=json.load(sys.stdin)["items"];print(len(p), sum(1 for x in p if x["metadata"].get("deletionTimestamp")))')
    [[ "$s" == "3 0" ]] && return 0; sleep 2
  done; echo "pods did not settle: $s"
}
W=$(mktemp -d)
"${n[@]}" set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED=false >/dev/null
"${n[@]}" rollout status deployment/aggregator --timeout=240s | tail -1; settle
echo "processing flag on live pods: $("${n[@]}" get pods -l app=aggregator -o jsonpath='{range .items[*]}{.spec.containers[0].env[?(@.name=="FIELD_REPORT_PROCESSING_ENABLED")].value} {end}')"
FIELD=$(login)
: > "$W/ids"
for i in $(seq 1 20); do
  code=$(curl -sS -o "$W/k" -w '%{http_code}' -X POST "$API/field-reports" -H "Authorization: Bearer $FIELD" \
    -H "Idempotency-Key: podkill-$i-$(date +%s%N)" -H 'Content-Type: application/json' --data-binary @contracts/examples/field-report.json)
  echo "$code $(python3 -c "import json;print(json.load(open('$W/k')).get('report_id',''))")" >> "$W/ids"
done
echo "accepted: $(awk '$1==202' "$W/ids" | wc -l)/20"
id1=$(awk '$1==202 {print $2; exit}' "$W/ids")
echo "before kill: $(curl -sS "$API/field-reports/$id1" -H "Authorization: Bearer $FIELD")"
KILL=$(date -u +%Y-%m-%dT%H:%M:%S)
"${n[@]}" delete pod -l app=aggregator --grace-period=0 --force >/dev/null 2>&1
echo "all aggregator pods force-deleted at ${KILL}Z"
"${n[@]}" set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED- >/dev/null
"${n[@]}" rollout status deployment/aggregator --timeout=300s | tail -1; settle
sleep 15
FIELD=$(login)
: > "$W/status"
for id in $(awk '$1==202 {print $2}' "$W/ids"); do
  curl -sS "$API/field-reports/$id" -H "Authorization: Bearer $FIELD" >> "$W/status"; echo >> "$W/status"
done
python3 - "$W/status" "$KILL" <<'EOF'
import sys, json, collections
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
kill = sys.argv[2]
print('after resume, first:', json.dumps(rows[0]))
print('status counts:', dict(collections.Counter(r.get('status') for r in rows)))
print('attempts counts:', dict(collections.Counter(r.get('attempts') for r in rows)))
print('processed after kill:', sum(1 for r in rows if (r.get('processed_at') or '') > kill), '/', len(rows))
EOF
rm -rf "$W"
