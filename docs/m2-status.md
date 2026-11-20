# M2 implementation status

This tracks implementation and evidence for the M2 submission. Requirements refer to the M2 specification and the supplied coastal-sensor contract. See the [detailed checklist](m2-requirements-checklist.md) for individual criteria.

| Area | Available | Remaining |
|---|---|---|
| Platform | Four separate-kernel VMs; local storage; prescribed role labels/resources; deployment/restart scripts. | Tester Agent installation/practice scenarios and final demo environment. |
| P1: Yang Pulang Membawa Laptopnya | Four identity-hashed PostgreSQL units with three copies each, cross-unit reads, Audit API, and resumable CSV import. Browser analytics matched Audit across five categories in a fixture. | Mount the official catalog on every Aggregator. Compare against Audit and measure geographic skew, disk loss, and replacement on the assessment cluster. |
| P2: Dua Nakhoda, Satu Kapal | Three Aggregators; source-session ownership and fencing; read failover; worker-freeze and clock-skew checks. | Measure takeover against the formal limit; sustained 10,000 VU/120s; 3→5→2 scaling; 2× throughput; rolling-update load evidence. |
| P3: Surat dari Tetangga | Byte-preserving journal, durable cursor/plan/state, retry/park lifecycle, ordered replay, canonical receipts, PVMBG schema modes and replay; BNPB-only public Ops routes; durable idempotent Field Reports ([details](m2-field-reports.md)). | Incompatible PVMBG modes and parser adaptation demo; complete malformed/oversized HTTP-source experiments; live Field Report pod-kill demo. |
| P4: Atap Seng di Siang Bolong | SSE resume, fire recognition in feed order, transactional state and revisions. A 1,800-detection fixture passed through PostgreSQL and both JetStream consumers. | Add feed lag and intake-to-storage latency measurements. Verify live processing, 60× catch-up, singleton counts, and the assistant reference. |
| P5: Pelampung yang Berbohong | Pinned sensor, durable coastal ingestion and stale status, a retryable Field Team receiver, and reserved API capacity. | Reconcile the complete expected critical IDs and receipts, verify p95 ≤ 5 seconds, and exercise priority and mixed-fault recovery with the assessed configuration. |
| Observability | Prometheus/Grafana/Loki; bounded OTel export and Tempo; propagated HTTP/message trace context. | New-feature metrics/traces and workload sizing during acceptance. |
| Report/submission | Architecture and reproducible development checks. | M2 report, required evidence, demo, and release/submission artifacts. |

## Verification commands

Run inside the development control VM, from `~/aat`:

| Command | What it establishes |
|---|---|
| `python3 scripts/m2/check-foundation.py` | Disposable PostgreSQL protocol checks: ownership, byte/cursor atomicity, fencing, state/plan transactions, ordered replay, crash-after-commit deduplication, retractions, Audit pagination/checksum, and partial reads. |
| `python3 scripts/m2/check-pipeline.py` | Live Audit authentication, physical copies, event reads, and checksum availability. |
| `python3 scripts/m2/check-pipeline.py --exercise-recovery` | Temporarily paused processing, durable raw inputs across receiver replacement, resume, and checksum comparison against a quiescent scan. |
| `python3 scripts/m2/check-cluster.py` | Current placement, synchronous replicas, JetStream replication, source callers, API permissions, logs/metrics, and an HTTP/database trace. |
| `python3 scripts/m2/check-coastal.py` | Practice coastal reads, analytics comparison against Audit, and a sample of the Field Team receiver's durable receipts. |

Verification reports are written under ignored `.state/m2/`. Protocol tests use disposable databases, not application data. Passing these checks does not establish the remaining official workload, reference-dataset, or combined-failure criteria.

## Operational boundaries

- The verified four-VM setup runs on Refki's laptop (32 GiB RAM, 12 logical CPUs); teammates may use different machines. Worker loss is covered; physical-host/control-plane loss is outside this development topology's guarantee.
- Canonical writes and reads are per unit. Multi-unit delivery can partially commit and resume through receipts; there is no global transaction or cross-unit snapshot. Compare full checksums on a quiescent dataset.
- Partial pages have no continuation cursor; restart the scan after recovery. An unavailable unit must not be interpreted as an empty unit.
- Raw intake allows at most 10,000 unfinished records per source. HTTP bodies and normalized events have separate size limits. Completed raw inputs and history have no automatic cleanup; measure disk use with the official workload.
- A committed normalized plan is immutable during delivery retries. Parser fixes apply to inputs which failed before a plan committed. Processor state belongs in the supplied transaction, rather than a process-local cache.
- A local development Audit token is generated as a Secret. Replace it with the assistant-issued token for assessment. Supply the assistant-issued source tokens for the live satellite and coastal runs.
