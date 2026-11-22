#!/usr/bin/env bash
# P3.5 and P3.6 live demo on the k3s cluster. Run inside aat-dev from ~/aat.
set -uo pipefail
export KUBECONFIG=~/.kube/aat-dev.yaml
n=(kubectl --context aat-dev -n sistem-informasi-kebencanaan-bnpb)
svc() { "${n[@]}" get svc "$1" -o jsonpath='{.spec.clusterIP}:{.spec.ports[0].port}'; }
AUTH="http://$(svc auth)"; API="http://$(svc client-api)"
secret() { python3 -c "import json;print(json.load(open('.state/m2/credentials.json'))['$1'])"; }
login() { curl -sS -X POST "$AUTH/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"$1\",\"client_secret\":\"$(secret "$2")\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])'; }
FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET)
MEDIA=$(login media MEDIA_CLIENT_SECRET)
OPS=$(login bnpb-internal BNPB_INTERNAL_CLIENT_SECRET)
BODY=contracts/examples/field-report.json
post() { curl -sS -o "$2" -D "$2.h" -w '%{http_code} %{time_total}' -X POST "$API/field-reports" \
  -H "Authorization: Bearer $FIELD" -H "Idempotency-Key: $1" -H 'Content-Type: application/json' --data-binary @"$BODY"; }
W=$(mktemp -d)

echo "== P3.6a latency: 100 reports, distinct keys"
: > "$W/lat"
for i in $(seq 1 100); do
  [[ $i == 50 ]] && FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET)
  post "lat-$RANDOM-$i-$(date +%s%N)" "$W/r" >> "$W/lat"; echo >> "$W/lat"
done
python3 - "$W/lat" <<'EOF'
import sys, collections
rows = [l.split() for l in open(sys.argv[1]) if l.strip()]
codes = collections.Counter(r[0] for r in rows); ms = sorted(float(r[1]) * 1000 for r in rows)
print(f"codes={dict(codes)} p50={ms[len(ms)//2]:.1f}ms p95={ms[int(len(ms)*0.95)-1]:.1f}ms max={ms[-1]:.1f}ms")
EOF

echo "== P3.6b retry with same key"
FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET)
KEY="retry-$(date +%s%N)"
for a in 1 2; do
  echo "attempt $a: $(post "$KEY" "$W/retry$a") report_id=$(python3 -c "import json;print(json.load(open('$W/retry$a'))['report_id'])") $(grep -i '^idempotent-replayed' "$W/retry$a.h" | tr -d '\r')"
done

echo "== P3.6c accept while processing is stopped, kill receivers after 202"
"${n[@]}" set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED=false >/dev/null
"${n[@]}" rollout status deployment/aggregator --timeout=240s | tail -1
FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET)
: > "$W/ids"
for i in $(seq 1 20); do
  code=$(post "kill-$i-$(date +%s%N)" "$W/k")
  echo "$code $(python3 -c "import json;print(json.load(open('$W/k')).get('report_id',''))")" >> "$W/ids"
done
echo "accepted: $(awk '$1==202' "$W/ids" | wc -l)/20"
"${n[@]}" delete pod -l app=aggregator --grace-period=0 --force >/dev/null 2>&1
echo "all aggregator pods force-deleted at $(date -u +%T)"
"${n[@]}" set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED- >/dev/null
"${n[@]}" rollout status deployment/aggregator --timeout=300s | tail -1
sleep 20
FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET)
: > "$W/status"
for id in $(awk '$1==202 {print $3}' "$W/ids"); do
  curl -sS "$API/field-reports/$id" -H "Authorization: Bearer $FIELD" >> "$W/status"; echo >> "$W/status"
done
python3 - "$W/status" <<'EOF'
import sys, json, collections
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
print('first status:', json.dumps(rows[0]))
print('status counts:', dict(collections.Counter(r.get('status') for r in rows)))
print('attempts counts:', dict(collections.Counter(r.get('attempts') for r in rows)))
EOF

echo "== P3.5 failed-message access: media, field-team, bnpb-internal"
FIELD=$(login field-team FIELD_TEAM_CLIENT_SECRET); MEDIA=$(login media MEDIA_CLIENT_SECRET); OPS=$(login bnpb-internal BNPB_INTERNAL_CLIENT_SECRET)
for t in MEDIA FIELD OPS; do
  echo "$t: $(curl -sS -o "$W/fm" -w '%{http_code}' "$API/failed-messages?limit=5" -H "Authorization: Bearer ${!t}")"
done
python3 -c "import json;b=json.load(open('$W/fm'));print('ops list items:',len(b.get('items',[])),'first:',json.dumps((b.get('items') or [None])[0])[:300])"
rm -rf "$W"
