# k3s development

Single-node development deployment of the existing M1 services, with metrics and logs in Grafana. Multi-node storage, replication, and coordinated Aggregators are later work.

## Run

From a checkout inside a dedicated Ubuntu 24.04 VM with sudo access:

```sh
bash scripts/m2/setup-vm.sh
bash scripts/m2/deploy.sh
python3 scripts/m2/check.py
```

Setup installs k3s, Helm, and Buildah. Deployment builds the service images inside the VM, imports them into k3s, generates private credentials, and deploys the applications and monitoring. Subsequent deployments reuse credentials and persistent data.

The check verifies source ingestion, access restrictions, consumer delivery, metrics, correlated logs, and denied direct PostgreSQL access from the API role. Results are saved to `.state/m2/check.json`.

## Open and inspect

```sh
export KUBECONFIG="$HOME/.kube/aat-dev.yaml"
kubectl --context aat-dev get nodes -o wide
kubectl --context aat-dev -n sistem-informasi-kebencanaan-bnpb get pods
```

Using the node's internal IP from the first command:

| Service | URL |
|---|---|
| Grafana | `http://<node-ip>:30000` |
| Client API | `http://<node-ip>:30080` |
| Auth | `http://<node-ip>:30084` |

Grafana provides a local Viewer dashboard. Application credentials and JWT keys are generated under ignored `.state/m2/` and injected as Kubernetes Secrets. Only Auth receives the private signing key. Database and broker ports remain internal.

Edit `.state/m2/runtime.env` for non-secret settings, then rerun deployment. Listener port changes also require corresponding Service and probe changes. Startup settings are applied by recreating the development pods.

For Multipass, stop/start the VM without deleting its data:

```sh
multipass stop aat-dev
multipass start aat-dev
```
