# Coastal alerts and analytics

The replicated profile polls the pinned Sensor Pantai image through one elected Aggregator. It commits each raw observation and the next cursor together. The processor joins observations with the station catalog captured at intake. It stores valid observations as `COASTAL` and parks invalid inputs with a reason. The sensor's `critical` flag determines alert eligibility. Canonical severity is `AWAS` for critical observations and `WASPADA` for other valid observations.

Client API reads the stored data during sensor faults. The source is marked stale if the newest observation is over two minutes behind the sensor's reported time, or if observations stop advancing for 30 seconds of wall time. These clocks are separate because practice uses simulated time. Requests time out after one second. Repeated failed or non-advancing polls back off from one to eight seconds with jitter. The next successful poll resumes from the committed cursor.

## Start the practice sensor

On the development control VM, initialize the namespace and create the practice Secret once:

```sh
export KUBECONFIG=~/.kube/aat-dev.yaml
kubectl apply -f infra/k8s/dev/namespace.yaml
openssl rand -hex 32 | tr -d '\n' | kubectl -n sistem-informasi-kebencanaan-bnpb create secret generic pantai-token --from-file=token=/dev/stdin
python3 scripts/m2/prepare.py
```

Set `PANTAI_BASE_URL=http://sensor-pantai:8080` in `.state/m2/runtime.env` and run:

```sh
bash scripts/m2/deploy-cluster.sh
python3 scripts/m2/check-coastal.py
```

If Aggregator was already deployed and only its environment changed, run `kubectl -n sistem-informasi-kebencanaan-bnpb rollout restart deployment/aggregator` and wait for the rollout before checking. The same restart applies after rotating the sensor token.

The deployment keeps sensor data, the journal cursor, and the receiver inbox on their own volumes across pod restarts. The sensor and receiver use node-local volumes on the control node in Refki's tested four-VM setup. Other hosts can use the same placement rules. The course exam uses the assistant's supplied key and exam profile; update the sensor manifest and Secret for that run.

## Field Team delivery

Notifier sends critical coastal events to the separate HTTP receiver. The receiver writes the original envelope to its own persistent inbox before returning 204. An observation has one receipt even if JetStream redelivers it or a later revision arrives. A failed receiver request leaves the message unacknowledged for retry. This guarantee is bounded by JetStream retention and the availability of the receiver's volume.

Use the receiver's authenticated `GET /v1/notifications` to inspect its latest 100 receipts. The development check reads the inbox without exposing tokens. Grafana shows delivery attempts, receipts and p95 from raw intake to the durable receipt. This latency includes mapping, storage, outbox, NATS, and receiver delivery. Sensor polling delay and the course's full critical-event loss and latency targets require a separate integrated measurement.

## Analytics

Open `http://<control-node-ip>:30080/analytics/` and use an access token from Auth. Select a time range using occurrence times. Dates on the chart are shown in UTC. Aggregator counts active records once per shard, then merges the counts and identifies missing units. Severity and area totals count each canonical record once. The tsunami series counts seismic records with tsunami warnings, so those records appear in both the earthquake and tsunami series.

`check-coastal.py` compares a historical window against Audit and checks the receiver contract. Use a stable event window for exact comparison because each shard is read independently. The disposable PostgreSQL checks exercise partial counts when a unit is unavailable.
