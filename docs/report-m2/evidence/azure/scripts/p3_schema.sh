#!/usr/bin/env bash
# P3.1, P3.2, P3.4 live: incompatible PVMBG mode -> parked, adapt, replay via Ops.
# Run inside aat-dev from ~/aat.
set -uo pipefail
export KUBECONFIG=~/.kube/aat-dev.yaml
n=(kubectl --context aat-dev -n sistem-informasi-kebencanaan-bnpb)
svc() { "${n[@]}" get svc "$1" -o jsonpath='{.spec.clusterIP}:{.spec.ports[0].port}'; }
AUTH="http://$(svc auth)"; API="http://$(svc client-api)"
secret() { python3 -c "import json;print(json.load(open('.state/m2/credentials.json'))['$1'])"; }
OPS() { curl -sS -X POST "$AUTH/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"bnpb-internal\",\"client_secret\":\"$(secret BNPB_INTERNAL_CLIENT_SECRET)\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])'; }
"${n[@]}" port-forward svc/pvmbg-mock 18082:8082 --address 127.0.0.1 >/dev/null 2>&1 & PF=$!
sleep 3
mode() { curl -sS -w ' %{http_code}\n' -X POST http://127.0.0.1:18082/admin/schema-version \
  -H "Authorization: Bearer $(secret PVMBG_TOKEN)" -H 'Content-Type: application/json' -d "{\"mode\":\"$1\"}"; }
failed() { curl -sS "$API/failed-messages?source=PVMBG&limit=200" -H "Authorization: Bearer $1"; }
summ() { python3 -c '
import json,sys,collections
b=json.load(sys.stdin); items=b.get("items",[])
print("failed PVMBG:",len(items),dict(collections.Counter((i.get("status"),i.get("retryable")) for i in items)))
print("example:",json.dumps(items[0])[:400] if items else None)'; }

echo "== before: $(python3 audit_snapshot.py p3-before | python3 -c 'import json,sys;d=json.load(sys.stdin);print("PVMBG",d["PVMBG"]["count"],"dups",d["duplicate_source_refs"])')"
echo "== switch PVMBG mock to rename_field (incompatible)"; mode rename_field
sleep 45
T=$(OPS); failed "$T" | summ
echo "== client-api reads PVMBG still answer: $(curl -sS -o /dev/null -w '%{http_code}' "$API/v1/hazards?source=PVMBG&limit=5" -H "Authorization: Bearer $T")"
echo "== adapt: PVMBG_SCHEMA_ADAPTERS=level_alias"
"${n[@]}" set env deployment/aggregator PVMBG_SCHEMA_ADAPTERS=level_alias >/dev/null
"${n[@]}" rollout status deployment/aggregator --timeout=300s | tail -1
sleep 10
T=$(OPS)
ids=$(failed "$T" | python3 -c 'import json,sys;print(" ".join(i["message_id"] for i in json.load(sys.stdin).get("items",[]) if i.get("status")=="parked"))')
echo "replaying $(wc -w <<<"$ids") parked messages"
codes=""
for id in $ids; do codes+="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/failed-messages/$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$id")/replay" -H "Authorization: Bearer $T") "; done
echo "replay codes: $(tr ' ' '\n' <<<"$codes" | sort | uniq -c | tr '\n' ' ')"
sleep 30
T=$(OPS); failed "$T" | summ
echo "== after: $(python3 audit_snapshot.py p3-after | python3 -c 'import json,sys;d=json.load(sys.stdin);print("PVMBG",d["PVMBG"]["count"],"dups",d["duplicate_source_refs"])')"
echo "== restore mock to compatible_v1"; mode compatible_v1
kill $PF
