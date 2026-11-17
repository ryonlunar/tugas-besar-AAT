# k3s development

Single-node development deployment of the existing M1 services, with metrics and logs in Grafana. Replicated storage and coordinated Aggregators are described in the [M2 design](../../docs/m2-foundation.md) and are not deployed by this profile.

## Prepare Ubuntu

Use a dedicated **Ubuntu 24.04 x86_64 VM** with sudo access, internet connectivity, 4 vCPUs, 8 GiB RAM, and 30 GiB disk. Multipass is one option; VirtualBox, VMware, or another VM tool can provide the same Ubuntu environment. The setup scripts run inside Ubuntu and do not depend on Multipass.

If using another tool, put this repository version in `~/aat`, make the VM reachable from the host or forward the ports listed below, then continue with **Run inside Ubuntu**.

### Multipass example

[Install Multipass](https://canonical.com/multipass/docs/latest/how-to-guides/install-multipass/) for your host OS. To choose another drive for VM disks, configure the [storage location](https://canonical.com/multipass/docs/latest/how-to-guides/customise-multipass/configure-where-multipass-stores-external-data/) before creating the VM.

On the host, create the VM:

```sh
multipass version
multipass launch 24.04 --name aat-dev --cpus 4 --memory 8G --disk 30G
```

From the host repository root, copy its committed runtime files into the VM:

```sh
git archive --format=tar.gz --output=../aat-runtime.tar.gz HEAD go.mod go.sum .env.example .dockerignore contracts pkg services infra scripts docs/m2-foundation.md
multipass transfer ../aat-runtime.tar.gz aat-dev:aat-runtime.tar.gz
multipass shell aat-dev
```

Inside Ubuntu, extract the source:

```sh
mkdir -p ~/aat
tar -xzf ~/aat-runtime.tar.gz -C ~/aat
```

The VM has its own source copy. The archive includes committed files only; host edits do not appear in the VM automatically. A checkout cloned directly inside the VM can also be used.

## Run inside Ubuntu

```sh
cd ~/aat
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

Use the node's internal IP from the first command. With NAT port forwarding, use the host address and your mapped ports instead:

| Service | URL |
|---|---|
| Grafana | `http://<node-ip>:30000` |
| Client API | `http://<node-ip>:30080` |
| Auth | `http://<node-ip>:30084` |

Grafana provides a local Viewer dashboard. Application credentials and JWT keys are generated under ignored `.state/m2/` and injected as Kubernetes Secrets. Only Auth receives the private signing key. Database and broker ports remain internal.

Edit `.state/m2/runtime.env` for non-secret settings, then rerun deployment. Listener port changes also require corresponding Service and probe changes. Startup settings are applied by recreating the development pods.

## Resume after a restart

Start the existing VM in your VM tool. k3s starts automatically and restores deployed services and persistent data. Inside Ubuntu, run:

```sh
cd ~/aat
python3 scripts/m2/check.py
```

For Multipass, the equivalent host commands are:

```powershell
multipass start aat-dev
multipass info aat-dev
multipass exec aat-dev -- bash -lc "cd ~/aat && python3 scripts/m2/check.py"
```

Allow the services a moment to start. The check should finish with `"passed": true`; if SSH or Kubernetes is still starting, retry it after a minute. Use the current IPv4 from `multipass info` for the URLs above because it can change.

Setup and deployment do not need to run again after a restart. For code changes, update the VM source and run `bash scripts/m2/deploy.sh` from the repository root.

When finished, run `multipass stop aat-dev`. This preserves the VM and its data.
