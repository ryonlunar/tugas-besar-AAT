# M2 foundation

This document describes the M2 storage and ingestion design. The multi-node profile connects BMKG, PVMBG, Feed Satelit, and Sensor Pantai to four replicated storage units, three coordinated Aggregators, and replicated JetStream. It also serves the Audit API, Field Reports, and BNPB Ops routes. See the [handoff](m2-handoff.md) for entry points and ownership.

## Storage decision

Use PostgreSQL JSONB with four logical units, `shard-0` through `shard-3`. Aggregator owns their credentials and all canonical writes. Other services access its API, not its databases.

`partition.Unit(source, source_ref_id)` hashes the immutable identity into 96 buckets and assigns each bucket to a fixed unit. Coordinates and severity do not affect placement. Updating an event keeps its unit; changing physical nodes does not change its key. Changing the hash, bucket count, or unit assignment requires a migration.

| Approach | Decision |
|---|---|
| Hash event identity | Selected. Regional bursts spread across units; a single frequently updated event still targets one unit. |
| Partition by region or source | Rejected. A busy region or dominant source concentrates writes. |
| Citus | Viable alternative with SQL routing and online rebalancing. It still needs coordinator/worker HA, and explicit partial-result handling would add a separate read path. A small fixed unit map keeps this prototype on ordinary PostgreSQL, at the cost of implementing fan-out and pagination in Go. |

The verified development setup on **Refki's laptop (32 GiB RAM, 12 logical CPUs)** uses **four Linux VMs**: one control-plane/worker and three workers, with four PostgreSQL HA groups managed by the Zalando Postgres Operator and Patroni. Node-loss scenarios target workers; control-plane failure is outside this topology's guarantee. Each group has three instances on three distinct nodes; each node is excluded from one group. Thus each node holds only three quarters of canonical data, and every group retains two copies after any one node disappears. Synchronous replication requires one standby acknowledgement. Stable primary Services hide promotion from clients. Kubernetes labels and required anti-affinity must enforce the layout, rather than relying on preferred placement.

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
- Audit uses a separate `ingested_at ASC, hazard_id ASC` scan and SHA-256 over byte-sorted reference IDs. Location discovery lists the actual store pods and verifies the record on each physical PostgreSQL copy over TLS. Failed discovery/copies produce explicit partial metadata.

## Source ownership and durable intake

Each upstream source has one journal home: BMKG on shard-0, PVMBG on shard-1, SATELIT on shard-2, and SENSOR_PANTAI on shard-3. This placement concerns raw intake and processing progress; canonical events are independently hashed across all four units.

1. Acquire a dedicated PostgreSQL connection and a non-blocking advisory lock for that source.
2. Increment its durable generation and read its stored cursor.
3. Run the adapter while heartbeats keep that same connection alive. A heartbeat commits a generation-preserving update; a successful read from a former primary is insufficient. Polling adapters keep their loop inside `Run`; streaming adapters keep reading until cancellation or disconnect.
4. Append raw bytes and the next cursor atomically, checking generation and expected cursor in that transaction.
5. On connection/heartbeat failure, stop intake, close the session, and reacquire before reading again. Never reconnect using a cached generation or cursor.

Target settings are a 10-second server idle timeout, heartbeat every third of that timeout, 2-second database operation deadlines, and a jittered 2-second acquisition retry. Source HTTP deadlines remain independent. Ownership decisions do not compare client wall clocks. Event/log timestamps and freshness indicators still use wall clocks, so deliberate clock-skew experiments can distort those timestamps without changing ownership. PostgreSQL session locks provide exclusivity; its [idle timeout](https://www.postgresql.org/docs/16/runtime-config-client.html#GUC-IDLE-SESSION-TIMEOUT) releases an abandoned connection. See also [advisory locks](https://www.postgresql.org/docs/16/explicit-locking.html#ADVISORY-LOCKS).

Each source ownership session has durable intake and a separate processing loop. Before reading upstream, the new owner installs its generation on every canonical unit. Canonical transactions check both the original ownership session and a locked fence row; abandoned transactions have a two-second server timeout. Each unit commits changes, versions, an outbox entry, and a receipt together. The worker marks the journal input complete only after all affected units succeed. A partial cross-unit commit is retried through receipts.

Normalization runs in a transaction on the source's journal home. The processor may update recognition state through the supplied `pgx.Tx`; that state and the normalized plan commit together. The worker reuses a committed plan after a crash. A monotonic plan sequence prevents an older delivery retry from overwriting newer canonical state, including retractions of identities that do not yet exist. Validation and envelope-size checks happen before committing a plan, so failed mappings remain reparsable after a parser fix.

Failed inputs become `retrying` with bounded exponential delay for transient failures, or `parked` for invalid input requiring a change. Later inputs with the same `OrderingKey` wait behind an unfinished predecessor; unrelated keys continue. Replay durably requests another attempt without creating another input. Source freshness advances when the latest fetch succeeded and no unfinished inputs remain; unresolved parked inputs therefore keep the source stale.

This relies on PostgreSQL exposing one writable primary. A generation check does not fence a split-brain database by itself. Failover tests must verify that the former primary rejects writes after promotion of a replacement. HTTP reads may repeat after a crash before commit; durable record keys and cursor replay prevent duplicate effects. This is not an exactly-once network-delivery claim.

## Feature interfaces

| Component | Input and output |
|---|---|
| Satellite ingestion | Implement `contracts.SourceAdapter`; persist offset-addressed `detection`, `update`, and `retract` messages through the commit callback. Stateful recognition produces stable fire identities and `MutationUpsert`/`MutationRetract`. Keep detection coordinates and keys exactly as received. |
| PVMBG ingestion | Keep the existing compatible mapping. Add incompatible fixtures/modes; raw malformed records must reach the journal before decoding. |
| Reports and operations API | `POST /field-reports` accepts only Tim Lapangan and returns 202 after durable acceptance, with a receipt ID. Require an idempotency key; reusing it with different bytes returns 409. Failed-message inspection and replay are restricted to BNPB Internal Ops. |
| Shared foundation | Owns journal, processing transactions/checkpoints, canonical persistence, outbox, and replay ordering. Feature code does not assign canonical revisions or publish directly to JetStream. |

`RawRecord.Payload` is `[]byte`, not `json.RawMessage`, so invalid JSON and UTF-8 remain recoverable. A raw key identifies an immutable message: a satellite feed offset is suitable. For repeated snapshots, set `Snapshot: true`, supply a stable `OrderingKey`, and use `contracts.RawKey(upstreamID, payload)` as the content key. The journal suppresses unchanged consecutive versions and namespaces changed versions by their intake cursor, allowing a value to return to earlier content. Without a trustworthy ID, a content-derived ordering key isolates that input. Downstream processing deduplicates by domain identity separately. Reusing an immutable raw key with different bytes is rejected without advancing the cursor.

`Batch.From` is the resume position used for the request; `Batch.Cursor` is the next position. Commit must succeed before the adapter advances. On an ambiguous commit, reacquire and read the durable cursor instead of guessing. The runnable example is `go test ./contracts -run Example -v`.

Raw intake is capped at 10,000 unfinished records per source. The polling adapter pauses before fetching when that cap is reached; append also checks capacity transactionally. HTTP body limits remain configurable; the default processor additionally bounds individual records to 1 MiB, rejects invalid UTF-8 and excessive nesting, and validates normalized wire size against the configured broker limit. Oversized HTTP bodies are rejected before admission without advancing the cursor. Completed raw inputs, plans, receipts, and history are retained; automated cleanup is not implemented.

Canonical source/type pairs are BMKG/SEISMIC, PVMBG/VOLCANIC, SATELIT/WILDFIRE, and SENSOR_PANTAI/COASTAL. Keep BMKG's M1 tsunami representation; the future analytics tsunami series must explicitly derive from its warning attributes, since the specification does not add a `TSUNAMI` canonical type. Synthetic coastal fixtures demonstrate shape, not severity-mapping thresholds.

The JetStream v1 envelope supports `hazard.created`, `hazard.updated`, and `hazard.retracted`. A retraction keeps the canonical identity/history and emits a new revision carrying the hazard snapshot. Active reads and Audit scans exclude retracted records. Re-activation reuses the original identity and increments its revision. Field Reports use a separate intake path; they do not add a `HazardEvent.source` value.

## API and configuration names

| Route | Access | Status |
|---|---|---|
| Existing `/v1/hazards` routes | Media summary; Tim Lapangan/BNPB Internal full reads. | Implemented |
| `POST /field-reports`, `GET /field-reports/{report_id}` | Tim Lapangan (`reports:write`), with server-side priority over Media. | Implemented |
| `GET /failed-messages`, `GET /failed-messages/{message_id}`, `POST /failed-messages/{message_id}/replay` | BNPB Internal Ops (`ops:failed-messages`) through Client API. | Implemented |
| `/internal/v1/failed-messages` and its detail/replay routes | Internal service credential; raw bytes are base64 in detail. | Implemented in Aggregator |
| `/internal/audit/events`, `/checksum`, `/location`, `/unprocessed` under `/internal/audit` | Separate audit bearer token, following Appendix A. | Implemented |

Preserve the existing auth routes and 60-second access-token TTL. Extend server-side permissions when the new routes are implemented; a UI restriction is insufficient.

The field-report body follows [field-report.schema.json](../contracts/field-report.schema.json), with a [runnable validation fixture](../contracts/examples/field-report.json): `go test ./contracts -run TestSchemasAndExamples -v`. Require the `Idempotency-Key` header (1–128 ASCII letters, digits, `.`, `_`, `:`, or `-`), scoped to the authenticated client; persist the original bytes, request hash, and receipt together before 202. The response is `{"report_id":"<uuid>","status":"accepted"}`. Broker duplicate windows alone are insufficient for durable idempotency. A report is independently stored input, not automatically a new canonical hazard.

Configure the satellite adapter with `SATELLITE_BASE_URL` and the `satellite-secret` Secret. The [coastal profile](../infra/k8s/README.md#sensor-pantai-and-analytics) uses `PANTAI_BASE_URL` and the `pantai-token` Secret. Tokens belong in Secrets and ordinary settings in ConfigMaps. Active Audit, processing, sharding, ownership, broker-replication, and tracing variables are listed in `.env.example`; the single-node and Compose paths remain the defaults when `SHARD_CONFIG_FILE` is empty.

## Run the storage protocol checks

Follow the [development setup guide](../infra/k8s/README.md) first, then run from the repository root inside Ubuntu:

```sh
python3 scripts/m2/check-foundation.py
```

This uses the existing Go/Buildah toolchain and creates four databases in a temporary PostgreSQL pod on the control node. Application databases are not modified. Tests cover exclusive source ownership, idle-owner takeover, rejected stale writes, raw-byte preservation, atomic cursor progress, deduplication, heartbeat continuity, hashed placement, ordered pagination, and partial reads. The temporary database pod, credentials, and Go test container are removed afterward. These tests also exercise canonical routing, precision-preserving reads, lost-owner fencing, and idempotent outbox acknowledgements. They also check processor-state rollback, saved-plan reuse after a crash, ordered replay, tombstones/re-activation, and Audit checksums/pagination. They use disposable databases on one server; physical-node failure checks require the multi-node profile. See [M2 status](m2-status.md) for live recovery checks.
