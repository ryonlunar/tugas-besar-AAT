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

Metrics are scraped every 5 seconds; the dashboard refreshes every 10 seconds and shows up to 200 log lines per query. Successful idle broker fetches and empty outbox checks use DEBUG. Event-delivery logs and failures remain visible at the default INFO level, with IDs kept in log fields. Prometheus and Loki retain 24 hours; Prometheus also has a 512 MB block-retention target. These settings do not impose a hard disk quota. Storage capacity must be measured under the M2 workload. Distributed tracing is not installed yet.

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

## Resume after a laptop restart

In Windows PowerShell:

```powershell
multipass start aat-dev
multipass info aat-dev
multipass exec aat-dev -- bash -lc "cd /home/ubuntu/aat && python3 scripts/m2/check.py"
```

Allow the services a moment to start. The check should finish with `"passed": true`; if SSH or Kubernetes is still starting, retry it after a minute. Use the current IPv4 from `multipass info` for the URLs above because it can change.

k3s starts automatically and restores the deployed services and persistent data. Setup and deployment do not need to run again after a restart. The VM source copy at `/home/ubuntu/aat` is separate from the host worktree; code changes must be copied there before redeployment.

When finished, run `multipass stop aat-dev`. This preserves the VM and its data.
