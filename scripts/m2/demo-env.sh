#!/usr/bin/env bash
# Source from the repository root on the control plane.
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/aat-dev.yaml}"
NS=sistem-informasi-kebencanaan-bnpb
k() { kubectl --context aat-dev -n "$NS" "$@"; }
NODE_IP=$(kubectl --context aat-dev get node aat-dev -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}') || return
API_URL="http://$NODE_IP:30080"
AUTH_URL="http://$NODE_IP:30084"
CREDS=.state/m2/credentials.json
EVIDENCE="${EVIDENCE:-.state/m2/verification-$(date -u +%Y%m%dT%H%M%SZ)}"
(umask 077; mkdir -p "$EVIDENCE") || return

login() {
  jq -c --arg client "$1" --arg key "$2" \
    '{client_id:$client,client_secret:.[$key]}' "$CREDS" |
    curl -fsS -H 'Content-Type: application/json' --data-binary @- "$AUTH_URL/v1/auth/login" |
    jq -er '.access_token'
}
field_login() { login field-team FIELD_TEAM_CLIENT_SECRET; }
ops_login() { login bnpb-internal BNPB_INTERNAL_CLIENT_SECRET; }
media_login() { login media MEDIA_CLIENT_SECRET; }
amati() {
  local token
  token=$(field_login) || return
  curl -sS --max-time 5 -w '\nHTTP %{http_code} time_total=%{time_total}s\n' \
    -H "Authorization: Bearer $token" "$API_URL$1"
}

audit() {
  local token
  token=$(jq -er '.AUDIT_TOKEN' "$CREDS") || return
  curl -fsS --max-time 10 -H "Authorization: Bearer $token" \
    "http://127.0.0.1:18083/internal/audit/$1"
}

runtime_set() {
  local key=$1 value=$2 envfile=.state/m2/runtime.env
  [[ $key =~ ^[A-Z][A-Z0-9_]*$ ]] || return 2
  sed -i "/^$key=/d" "$envfile" && printf '%s=%s\n' "$key" "$value" >>"$envfile"
}
