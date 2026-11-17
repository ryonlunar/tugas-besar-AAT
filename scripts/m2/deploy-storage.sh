#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/aat-dev.yaml}"
namespace=sistem-informasi-kebencanaan-bnpb
[[ $(kubectl --context aat-dev get nodes -l aat.dev/environment=development -o name | wc -l) -eq 4 ]] || {
  echo 'Four ready development nodes are required.' >&2; exit 1;
}
kubectl --context aat-dev wait nodes -l aat.dev/environment=development --for=condition=Ready --timeout=180s
kubectl --context aat-dev apply -f infra/k8s/dev/namespace.yaml
kubectl --context aat-dev -n "$namespace" apply -f infra/k8s/dev/network-policies.yaml -f infra/k8s/cluster/storage-policy.yaml -f infra/k8s/cluster/postgres-environment.yaml -f infra/k8s/cluster/patroni-recovery.yaml
helm upgrade --install postgres-operator postgres-operator --repo https://opensource.zalando.com/postgres-operator/charts/postgres-operator \
  --version 2.0.3 --kube-context aat-dev --namespace "$namespace" \
  -f infra/k8s/cluster/operator-values.yaml --wait --timeout 5m
kubectl --context aat-dev wait --for=condition=Established crd/postgresqls.acid.zalan.do --timeout=120s
python3 scripts/m2/prepare-cluster.py
kubectl --context aat-dev apply -f .state/m2/cluster-storage.json
python3 scripts/m2/prepare-cluster.py configure-recovery
for unit in shard-0 shard-1 shard-2 shard-3; do
  kubectl --context aat-dev -n "$namespace" wait --for=create "statefulset/$unit" --timeout=180s
  kubectl --context aat-dev -n "$namespace" wait "statefulset/$unit" --for=jsonpath='{.status.readyReplicas}'=3 --timeout=600s
done
python3 scripts/m2/prepare-cluster.py credentials
kubectl --context aat-dev apply -f .state/m2/cluster-credentials.json
kubectl --context aat-dev -n "$namespace" get postgresqls
