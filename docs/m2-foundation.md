# M2 foundation

This document describes the M2 storage and ingestion design. The multi-node profile connects BMKG/PVMBG ingestion to four replicated storage units, three coordinated Aggregators, and replicated JetStream. Raw journaling remains a tested library; the processing/replay path and new M2 sources are not connected yet.

## Storage decision

Use PostgreSQL JSONB with four logical units, `shard-0` through `shard-3`. Aggregator owns their credentials and all canonical writes. Other services access its API, not its databases.

`partition.Unit(source, source_ref_id)` hashes the immutable identity into 96 buckets and assigns each bucket to a fixed unit. Coordinates and severity do not affect placement. Updating an event keeps its unit; changing physical nodes does not change its key. Changing the hash, bucket count, or unit assignment requires a migration.

| Approach | Decision |
|---|---|
| Hash event identity | Selected. Regional bursts spread across units; a single frequently updated event still targets one unit. |
| Partition by region or source | Rejected. A busy region or dominant source concentrates writes. |
| Citus | Viable alternative with SQL routing and online rebalancing. It still needs coordinator/worker HA, and explicit partial-result handling would add a separate read path. A small fixed unit map keeps this prototype on ordinary PostgreSQL, at the cost of implementing fan-out and pagination in Go. |

The multi-node target is **four Linux VMs**: one control-plane/worker and three workers, with four PostgreSQL HA groups managed by the Zalando Postgres Operator and Patroni. Node-loss scenarios target workers; control-plane failure is outside this topology's guarantee. Each group has three instances on three distinct nodes; each node is excluded from one group. Thus each node holds only three quarters of canonical data, and every group retains two copies after any one node disappears. Synchronous replication requires one standby acknowledgement. Stable primary Services hide promotion from clients. Kubernetes labels and required anti-affinity must enforce the layout, rather than relying on preferred placement.

A replacement node takes the vacant placement slot and rebuilds its three replicas from survivors. A controlled switchover can redistribute primary work without rehashing records. This handles replacement capacity; adding more logical units requires an explicit migration. Local volumes stay on their assigned nodes. Patroni first attempts `pg_rewind`; if rewind fails or a divergent timeline cannot be followed, it rebuilds that replica from the current primary. Deployment applies these recovery flags to Patroni's DCS because the operator CRD does not expose them. Losing a disk requires provisioning a replacement replica from a surviving copy; an existing local volume cannot move to another machine.

Patroni uses synchronous mode with `synchronous_mode_strict=true` and one standby acknowledgement. A group blocks writes when it cannot retain that extra copy. This trades write availability and latency for durability; it is not a guarantee against simultaneous loss of two nodes. The operator stores leader election state in Kubernetes ConfigMaps, without a separate etcd deployment. See [Patroni replication modes](https://patroni.readthedocs.io/en/latest/replication_modes.html).

CloudNativePG was considered, but version 1.30.1 forcibly sets the pod label `role` to `primary` or `replica`. That conflicts with M2's required `role=store`. The selected operator supports a separate `postgres-role` label and required pod anti-affinity through its [configuration](https://github.com/zalando/postgres-operator/blob/v2.0.3/charts/postgres-operator/values.yaml). Citus capabilities are described in its [cluster management guide](https://docs.citusdata.com/en/stable/admin_guide/cluster_management.html).

## Reads and consistency

Client API discovers Aggregator replicas through the `aggregator-peers` headless Service. It caches DNS answers for five seconds, rotates across replicas, and retries transport failures against up to three distinct replicas within the original request deadline. Each attempt reserves time for the remaining replicas. The M2 profile allows two seconds overall; storage fan-out retains its separate 200 ms unit timeout. HTTP rejection responses, including 429, are returned without retrying; this avoids multiplying load during admission control. A graceful shutdown gives DNS caches eight seconds to drain.

In this profile, Client API readiness is local: an upstream failure does not remove the API's Service endpoint. Requests remain available for authentication checks and explicit partial results or structured dependency errors. Upstream health is reflected by request results and metrics. The single-node profile retains its existing dependency readiness check.


- Query each unit's primary concurrently with a shared deadline. Source-state reads run alongside the event query, rather than extending its timeout serially. Return healthy results with `partial: true` and `unavailable_units` when a unit fails.
- The reader orders by `occurred_at DESC, hazard_id ASC` and uses keyset pagination. Cursors are bound to the source filter and unit set.
- A partial page has no continuation cursor. Restart the scan after recovery to include records from the recovered unit; continuing past them could silently omit data.
- Normal reads are committed primary reads per unit, without a global snapshot across units. Concurrent updates can change a multi-page scan. Complete checksum comparisons require a quiescent dataset or a future pinned snapshot.
- The Audit API needs its own ascending `ingested_at, hazard_id` scan, byte-ordered checksum, and live physical locations. It is not implemented by the occurrence-ordered reader.

## Source ownership and durable intake

Each upstream source has one journal home: BMKG on shard-0, PVMBG on shard-1, SATELIT on shard-2, and SENSOR_PANTAI on shard-3. This placement concerns raw intake and processing progress; canonical events are independently hashed across all four units.

1. Acquire a dedicated PostgreSQL connection and a non-blocking advisory lock for that source.
2. Increment its durable generation and read its stored cursor.
3. Run the adapter while heartbeats keep that same connection alive. A heartbeat commits a generation-preserving update; a successful read from a former primary is insufficient. Polling adapters keep their loop inside `Run`; streaming adapters keep reading until cancellation or disconnect.
4. Append raw bytes and the next cursor atomically, checking generation and expected cursor in that transaction.
5. On connection/heartbeat failure, stop intake, close the session, and reacquire before reading again. Never reconnect using a cached generation or cursor.

Target settings are a 10-second server idle timeout, heartbeat every third of that timeout, 2-second database operation deadlines, and a jittered 2-second acquisition retry. Source HTTP deadlines remain independent. Ownership decisions do not compare client wall clocks. Event/log timestamps and freshness indicators still use wall clocks, so deliberate clock-skew experiments can distort those timestamps without changing ownership. PostgreSQL session locks provide exclusivity; its [idle timeout](https://www.postgresql.org/docs/16/runtime-config-client.html#GUC-IDLE-SESSION-TIMEOUT) releases an abandoned connection. See also [advisory locks](https://www.postgresql.org/docs/16/explicit-locking.html#ADVISORY-LOCKS).

The running BMKG/PVMBG path uses this ownership session around its existing polling loops. Before polling, a new owner installs its generation on every canonical unit. Before polling and within each write transaction, verify the original ownership connection. Each transaction also locks its fence row and checks the generation. A two-second server timeout releases an abandoned transaction. Source freshness advances on its journal home after writes to the other units succeed. A cycle is not atomic across units: a failed cycle can leave a committed subset, which the next poll reconciles idempotently.

This relies on PostgreSQL exposing one writable primary. A generation check does not fence a split-brain database by itself. Failover tests must verify that the former primary rejects writes after promotion of a replacement. HTTP reads may repeat after a crash before commit; durable record keys and cursor replay prevent duplicate effects. This is not an exactly-once network-delivery claim.

## Feature interfaces

| Component | Input and output |
|---|---|
| Satellite ingestion | Implement `contracts.SourceAdapter`; persist offset-addressed `detection`, `update`, and `retract` messages through the commit callback. Stateful recognition produces stable fire identities and `MutationUpsert`/`MutationRetract`. Keep detection coordinates and keys exactly as received. |
| PVMBG ingestion | Keep the existing compatible mapping. Add incompatible fixtures/modes; raw malformed records must reach the journal before decoding. |
| Reports and operations API | `POST /field-reports` accepts only Tim Lapangan and returns 202 after durable acceptance, with a receipt ID. Require an idempotency key; reusing it with different bytes returns 409. Failed-message inspection and replay are restricted to BNPB Internal Ops. |
| Shared foundation | Owns journal, processing transactions/checkpoints, canonical persistence, outbox, and replay ordering. Feature code does not assign canonical revisions or publish directly to JetStream. |

`RawRecord.Payload` is `[]byte`, not `json.RawMessage`, so invalid JSON and UTF-8 remain recoverable. A raw key identifies an immutable message: a satellite feed offset is suitable. For snapshots and coastal observations, use `contracts.RawKey(upstreamID, payload)` so changed bytes under the same vendor ID remain inspectable instead of blocking intake. Without a trustworthy ID, pass an empty upstream ID. Downstream processing deduplicates by domain identity separately. Reusing an immutable raw key with different bytes is rejected without advancing the cursor.

`Batch.From` is the resume position used for the request; `Batch.Cursor` is the next position. Commit must succeed before the adapter advances. On an ambiguous commit, reacquire and read the durable cursor instead of guessing. The runnable example is `go test ./contracts -run Example -v`.

Raw intake is intended to be a bounded processing backlog. After processing and its resulting mutations are durably recorded, payloads can be removed while retaining deduplication identities. Failed/parked messages retain their full bytes until replay resolves them. Backlog limits, processing state, and completed-payload cleanup are not implemented yet.

Canonical source/type pairs are BMKG/SEISMIC, PVMBG/VOLCANIC, SATELIT/WILDFIRE, and SENSOR_PANTAI/COASTAL. Keep BMKG's M1 tsunami representation; the future analytics tsunami series must explicitly derive from its warning attributes, since the specification does not add a `TSUNAMI` canonical type. Synthetic coastal fixtures demonstrate shape, not severity-mapping thresholds.

The current JetStream v1 envelope still supports created/updated events only. `MutationRetract` is an internal processor contract; canonical deletion and a revisioned consumer tombstone must be implemented together before retractions are published. A missing fire must not be published as an ordinary update. Field reports use a separate intake path; do not invent another canonical `HazardEvent.source` value for them.

## API and configuration names

| Route | Access |
|---|---|
| Existing `/v1/hazards` routes | Media summary only; Tim Lapangan and BNPB Internal retain full reads. |
| `POST /field-reports` | Tim Lapangan only, with server-side admission priority over Media. |
| `GET /failed-messages` | BNPB Internal Ops only. |
| `POST /failed-messages/{message_id}/replay` | BNPB Internal Ops only; durable, idempotent replay request. |
| `GET /internal/audit/events`, `GET /internal/audit/checksum`, `GET /internal/audit/location`, `GET /internal/audit/unprocessed` | Aggregator validates the separate assistant-issued audit bearer token. Field names and ordering follow Appendix A. |

Preserve the existing auth routes and 60-second access-token TTL. Extend server-side permissions when the new routes are implemented; a UI restriction is insufficient.

The field-report body follows [field-report.schema.json](../contracts/field-report.schema.json), with a [runnable validation fixture](../contracts/examples/field-report.json): `go test ./contracts -run TestSchemasAndExamples -v`. Require the `Idempotency-Key` header (1–128 ASCII letters, digits, `.`, `_`, `:`, or `-`), scoped to the authenticated client; persist the original bytes, request hash, and receipt together before 202. The response is `{"report_id":"<uuid>","status":"accepted"}`. Broker duplicate windows alone are insufficient for durable idempotency. A report is independently stored input, not automatically a new canonical hazard.

Reserve `SATELLITE_BASE_URL`, `SATELLITE_TOKEN`, `PANTAI_BASE_URL`, `PANTAI_AUTH_TOKEN`, `AUDIT_TOKEN`, `SOURCE_SESSION_IDLE_TIMEOUT`, and `SOURCE_RETRY_INTERVAL`. Tokens belong in Secrets, ordinary settings in ConfigMaps. These are planned names, not active environment options; add them to `.env.example` and injection code when their consumers are implemented. Active sharding, ownership, broker-replication, and tracing variables are listed in `.env.example`; the single-node and Compose paths remain the defaults when `SHARD_CONFIG_FILE` is empty.

## Run the storage protocol checks

Follow the [development setup guide](../infra/k8s/README.md) first, then run from the repository root inside Ubuntu:

```sh
python3 scripts/m2/check-foundation.py
```

This uses the existing Go/Buildah toolchain and creates four databases in a temporary PostgreSQL pod on the control node. Application databases are not modified. Tests cover exclusive source ownership, idle-owner takeover, rejected stale writes, raw-byte preservation, atomic cursor progress, deduplication, heartbeat continuity, hashed placement, ordered pagination, and partial reads. The temporary database pod, credentials, and Go test container are removed afterward. These tests also exercise canonical routing, precision-preserving reads, lost-owner fencing, and idempotent outbox acknowledgements. They use disposable databases on one server; physical-node failure checks require the multi-node profile.
