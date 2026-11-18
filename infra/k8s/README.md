# k3s development

Two development profiles share the existing Go services: a single-node M1 profile and a multi-node M2 profile. Both provide metrics and logs in Grafana; the multi-node profile also includes distributed tracing. Start with the [team handoff](../../docs/m2-handoff.md) for code entry points and remaining work; storage and ingestion decisions are in the [M2 design](../../docs/m2-foundation.md).

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
git archive --format=tar.gz --output=../aat-runtime.tar.gz HEAD go.mod go.sum .env.example .dockerignore contracts pkg services infra scripts docs/m2-foundation.md docs/m2-handoff.md docs/m2-status.md docs/m2-requirements-checklist.md docs/m2-coastal.md
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

Metrics are scraped every 5 seconds; the dashboard refreshes every 10 seconds and shows up to 200 log lines per query. Successful idle broker fetches and empty outbox checks use DEBUG. Event-delivery logs and failures remain visible at the default INFO level, with IDs kept in log fields. Prometheus and Loki retain 24 hours; Prometheus also has a 512 MB block-retention target. These settings do not impose a hard disk quota. Storage capacity must be measured under the M2 workload. This single-node profile does not deploy tracing.

## Multi-node profile

Use four Ubuntu VMs on a private network: `aat-dev` (4 vCPUs, 8 GiB) and `aat-worker-1` through `aat-worker-3` (2 vCPUs, 3 GiB each), with 30 GiB disk per VM. These are the reference allocations tested on Refki's Windows laptop (32 GiB RAM, 12 logical CPUs). Teammates may use different machines/providers while preserving kernel, placement, and resource requirements. This single-host setup does not provide physical-host fault tolerance.

With Python 3, Git, and Multipass installed on the host, run from this repository root:

```sh
python3 scripts/m2/multipass-cluster.py up
python3 scripts/m2/multipass-cluster.py deploy
multipass exec aat-dev -- bash -lc "cd ~/aat && python3 scripts/m2/check-cluster.py"
```

`up` creates or starts the VMs, installs k3s, copies runtime files, and configures node placement. `deploy` synchronizes local changes, builds immutable image tags, and deploys the stack. From WSL, pass `--multipass /path/to/multipass.exe` before the action if the Windows executable is not on PATH.

The deployment uses four PostgreSQL units with three replicas each, three Aggregators, and three NATS brokers. Broker connections use two-second heartbeats to detect silent peers. Each node is excluded from one storage unit. Only Aggregator receives application database credentials, with TLS certificate verification enabled. A local registry on port 30500 distributes development images over the trusted VM network; do not expose it publicly.

The first multi-node deployment initializes empty storage or imports existing M1 records, history, source status, and outbox entries while preserving IDs and revisions. During an import, the old writer is paused; original database and broker volumes are retained. Subsequent deployments skip this step using `aat-storage-cutover`; rolling application updates reuse the replicated stores. Returning to `deploy.sh` after cutover would start the old single-node dataset, so use `deploy-cluster.sh` for this environment afterward.

For another VM provider, use the same Ubuntu prerequisites and `configure-node.sh` to join the workers, then apply the placement labels used by `multipass-cluster.py`. The deployment itself runs inside Ubuntu:

```sh
cd ~/aat
bash scripts/m2/deploy-cluster.sh
python3 scripts/m2/check-cluster.py
```

After a laptop restart, run on the host:

```sh
python3 scripts/m2/multipass-cluster.py start
multipass exec aat-dev -- bash -lc "cd ~/aat && python3 scripts/m2/check-cluster.py"
```

`start` refreshes the control-plane address in each guest and waits for the replicated stores, brokers, and Aggregators to recover. No rebuild is required. `python3 scripts/m2/multipass-cluster.py stop` stops all four VMs and preserves their disks. Graceful shutdown can take several minutes; wait for the command to finish.

Tracing samples 1% of new traces and follows a parent's sampling decision. Tempo retains traces for one hour. Each service has a nonblocking queue capped at 512 spans; collector outages can drop telemetry without blocking ingestion. Health, readiness, metrics, and idle broker polling are excluded from tracing. Trace and correlation IDs remain log fields rather than indexed metric labels. Replicated storage gauges are deduplicated in the dashboard.

In Grafana, open **Explore**, select **Tempo**, choose **Trace ID**, and paste `checks.http_database_trace.trace_id` printed by `check-cluster.py`. That request is explicitly sampled; other log IDs are only stored when sampled. This profile allows Viewer access to Explore and temporary panel edits; changes cannot be saved.

`check-cluster.py` includes the basic checks and verifies placement, PostgreSQL synchronous replication, stream/consumer replicas, a steady-state upstream caller per source, keyset pagination, and a live HTTP-to-database trace. Node replacement, official catalog checksums, and full load/scaling acceptance require separate experiments.

For Sensor Pantai, Field Team receipts, and the analytics page, see the [coastal setup and checks](../../docs/m2-coastal.md).

## Durable ingestion and Audit checks

Inside the control VM:

```sh
cd ~/aat
python3 scripts/m2/check-foundation.py
python3 scripts/m2/check-pipeline.py
```

Add `--exercise-recovery` to the pipeline check to temporarily pause processing, replace one receiving Aggregator, verify preserved inputs, and restore processing. The check also compares a quiescent Audit event scan with the checksum. It reconnects its local port-forward across rollouts and saves recovery results under `.state/m2/check-pipeline-recovery.json`. Read-only checks use `check-pipeline.json`.

`INGEST_PROCESSING_ENABLED=false` pauses processing while durable intake continues, subject to the unfinished-record cap. Set it in `.state/m2/runtime.env` and redeploy to apply it persistently; restore `true` to drain the queue. `AUDIT_TOKEN` is generated in ignored `.state/m2/credentials.json` and injected only into Aggregator. Replace that value with the assistant-issued token and redeploy for assessment. Never place it in the ConfigMap or commit it.

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
