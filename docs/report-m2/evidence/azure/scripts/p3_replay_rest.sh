#!/usr/bin/env bash
# Replay every remaining parked PVMBG message via the BNPB Ops route, then report.
set -uo pipefail
export KUBECONFIG=~/.kube/aat-dev.yaml
n=(kubectl --context aat-dev -n sistem-informasi-kebencanaan-bnpb)
svc() { "${n[@]}" get svc "$1" -o jsonpath='{.spec.clusterIP}:{.spec.ports[0].port}'; }
AUTH="http://$(svc auth)"; API="http://$(svc client-api)"
secret() { python3 -c "import json;print(json.load(open('.state/m2/credentials.json'))['$1'])"; }
OPS() { curl -sS -X POST "$AUTH/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"bnpb-internal\",\"client_secret\":\"$(secret BNPB_INTERNAL_CLIENT_SECRET)\"}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])'; }
for round in 1 2 3; do
  T=$(OPS)
  ids=$(curl -sS "$API/failed-messages?source=PVMBG&limit=200" -H "Authorization: Bearer $T" | python3 -c 'import json,sys;print(" ".join(i["message_id"] for i in json.load(sys.stdin).get("items",[]) if i.get("status")=="parked"))')
  echo "round $round: parked=$(wc -w <<<"$ids")"
  [[ -z "$ids" ]] && break
  codes=""
  for id in $ids; do codes+="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API/failed-messages/$id/replay" -H "Authorization: Bearer $T") "; done
  echo "  replay codes: $(tr ' ' '\n' <<<"$codes" | grep . | sort | uniq -c | tr '\n' ' ')"
  sleep 20
done
python3 audit_snapshot.py p3-final | python3 -c 'import json,sys;d=json.load(sys.stdin);print("final PVMBG",d["PVMBG"]["count"],"dups",d["duplicate_source_refs"],"partial",d["events_partial"])'
