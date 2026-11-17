# M2 team handoff

The shared foundation is ready for parallel feature work. BMKG/PVMBG already run through durable intake, processing/replay, replicated PostgreSQL, and JetStream. Audit API, metrics, logs, and tracing are available. Satellite/coastal adapters, public Field Report/Ops routes, and analytics remain to be built.

## Work sequence

1. Review and merge the foundation into `main`.
2. Each owner creates a feature branch and separate worktree from updated `main`, implements their part, runs focused tests, and opens a PR.
3. After the feature PRs are merged, run the complete system together: official datasets, load/scaling tests, combined-failure simulations, and final measurements. Then assemble the report and demo evidence.

**The deliverable for each owner now is a working feature PR with reproducible checks.** Full-system simulations and report writing follow integration. Keep brief design decisions and useful commands in the PR so they can be reused later.

## Feature ownership

| Owner | Main responsibility | PR deliverable |
|---|---|---|
| Hazim | Source ingestion and mapping: BMKG data import, PVMBG schema changes, satellite/fire recognition. | Adapters, mappers, durable recognition state, and representative fixtures/tests. |
| Bimo | Client-facing writes, Ops access, Auth permissions, and request priority. | Field Reports, authorized inspection/replay, and server-side Field Team priority. |
| Refki | Coastal sensor, notification delivery, analytics UI, and shared integration. | Coastal processing, actual alert delivery, cross-unit analytics, and deployment integration. |

### Hazim: source ingestion and mapping

- Prepare BMKG historical-data import while preserving upstream reference IDs. Use a representative fixture until the official catalog is available.
- Connect the satellite session/SSE interface with durable offsets. Implement event-time fire grouping, stable identities, exact detection keys, lateness handling, updates, and retractions.
- Add incompatible PVMBG schema modes and define what the parser accepts or parks. Cover malformed input and recovery after adapting the parser.

**Start at:** [adapter contracts](../contracts/ingestion.go), [existing raw readers](../services/aggregator/internal/poller/raw.go), [mapper](../services/aggregator/internal/mapper/), and [PVMBG mock](../services/pvmbg-mock/).

**Ready for PR when:** fixtures establish mapping/grouping correctness, cursor/state recovery, and schema-failure replay without duplicate effects. The existing internal replay API can be used before Bimo's public routes are integrated. Full reference comparisons and 60× catch-up measurements follow integration.

### Bimo: Field Reports, Ops, and priority

- Add public `POST /field-reports` for Tim Lapangan. Add an Aggregator-owned receipt table/internal intake endpoint; commit bytes, request hash, and receipt before returning 202. Scope `Idempotency-Key` to the authenticated client.
- Expose failed-input inspection/replay through Client API, restricted to BNPB Internal Ops. Reuse Aggregator's internal endpoints below and extend Auth permissions.
- Give Field Team requests server-side admission priority; reject excess Media traffic with controlled responses.

**Start at:** [Client API](../services/client-api/internal/api/), [Auth](../services/auth/internal/auth/), [Field Report schema](../contracts/field-report.schema.json), and [internal Ops API](../services/aggregator/internal/audit/http.go).

**Ready for PR when:** focused checks prove access restrictions, durable/idempotent report acceptance across receiver restart, authorized replay, and priority under contention. Field Reports are a separate resource, not a new `HazardEvent.source`; Client API accesses the owning service rather than PostgreSQL directly.

### Refki: coastal alerts, analytics, and integration

- Deploy the prescribed sensor image and implement authenticated station/observation intake, validation, cursor recovery, and sensor-fault handling.
- Extend Notifier to deliver to the actual Field Team receiver with durable retry/deduplication. The current Notifier stores an inbox only.
- Build the analytics UI with time-range, severity, and area views across storage units, including partial-result indicators. Derive tsunami counts from BMKG warning attributes; there is no canonical `TSUNAMI` type.
- Maintain shared storage/coordination, manifests, observability, and test tooling. Coordinate the final integrated simulations and evidence collection after feature PRs merge.

**Start at:** [Aggregator startup](../services/aggregator/cmd/server/main.go), [processing pipeline](../services/aggregator/internal/pipeline/), [Notifier](../services/notifier-consumer/), and [k3s manifests](../infra/k8s/cluster/).

**Ready for PR when:** fixtures cover coastal validation/recovery, actual receiver delivery, and analytics correctness/partial behavior. Grafana remains the operational dashboard; the analytics UI is a separate feature. Final critical-alert latency and combined-failure measurements follow integration.

## Start development

Run fixture/unit checks locally first:

```sh
go test ./contracts ./services/aggregator/... ./services/client-api/... ./services/auth/...
```

For the running stack, follow the [k3s setup and restart guide](../infra/k8s/README.md#multi-node-profile). The verified reference environment is four VMs on **Refki's Windows laptop (32 GiB RAM, 12 logical CPUs)**. Teammates may use other machines/providers while preserving the required kernel, placement, and resource boundaries.

Inside the control VM, from `~/aat`:

```sh
python3 scripts/m2/check-foundation.py
python3 scripts/m2/check-pipeline.py
python3 scripts/m2/check-cluster.py
```

These establish the existing baseline. Feature PRs add their own fixtures, run commands, and expected results. `check-foundation.py` uses disposable PostgreSQL databases; ordinary local Go tests skip those database checks.

## Shared implementation reference

| Need | Entry point |
|---|---|
| Add a streaming adapter | Implement `contracts.SourceAdapter`; use `pipeline.RunAdapter` inside `CoordinateSessions`. Preserve the upstream cursor unchanged. |
| Add a stateful processor | Use the supplied `pgx.Tx` for recognition state; return one final mutation per identity. State and normalized plan commit together. |
| Persist canonical changes | [store/input.go](../services/aggregator/internal/store/input.go), [sharded/input.go](../services/aggregator/internal/sharded/input.go). Storage assigns revisions and writes the outbox. |
| Add schema/state tables | Add a new migration; coordinate shared migration and source-registration changes with Refki. |

Aggregator's internal Ops routes require `X-Internal-Key: <AGGREGATOR_API_KEY>`:

- `GET /internal/v1/failed-messages?source=PVMBG&limit=100&cursor=...`
- `GET /internal/v1/failed-messages/{message_id}` returns metadata and original bytes as base64.
- `POST /internal/v1/failed-messages/{message_id}/replay` durably queues another attempt; completed inputs return `already_processed`.

The Audit token grants neither raw inspection nor replay. See [the foundation design](m2-foundation.md) for transaction, ordering, snapshot-key, and API details.

Use [M2 status](m2-status.md) for a short overview and the [requirements checklist](m2-requirements-checklist.md) to track implementation and final evidence against the specification. Each member contributes their feature explanation and results to the report after integration.
