# M2 implementation status

This tracks implementation and evidence for the M2 submission. Requirements refer to the M2 specification and the supplied coastal-sensor contract. See the [detailed checklist](m2-requirements-checklist.md) for individual criteria.

| Area | Available | Remaining |
|---|---|---|
| Platform | Four separate-kernel VMs, local storage, prescribed roles, and deployment scripts. The official Tester Agent ran on all four nodes. | Repeat setup on the demo cluster and load the exam key when issued. |
| P1: Yang Pulang Membawa Laptopnya | Four identity-hashed PostgreSQL units with three copies each, Audit API, a catalog image for all replicas, and resumable CSV import. Analytics matched Audit across five categories in a fixture. | Deploy with the official catalog. Compare against Audit and measure geographic skew, disk loss, and replacement. |
| P2: Dua Nakhoda, Satu Kapal | Three Aggregators, source-session ownership and fencing, and practice evidence for failover, 10,000 VU, scaling, throughput, and rolling updates. | Repeat with the course workload and show the measured scope of each result. |
| P3: Surat dari Tetangga | Byte-preserving journal, durable cursor/plan/state, retry/park lifecycle, ordered replay, canonical receipts, PVMBG schema modes and replay; BNPB-only public Ops routes; durable idempotent Field Reports ([details](m2-field-reports.md)). | Incompatible PVMBG modes and parser adaptation demo; complete malformed/oversized HTTP-source experiments; live Field Report pod-kill demo. |
| P4: Atap Seng di Siang Bolong | SSE resume, transactional recognition state, and feed lag and intake-to-storage panels. The deployed Aggregator processed live practice batches at 60×. | Measure catch-up and p95 with the official workload, count singletons, and compare the complete result with the assistant reference. |
| P5: Pelampung yang Berbohong | Pinned sensor, durable coastal intake, retryable Field Team receiver, reserved API capacity, and a paginated critical-receipt checker. | Reconcile the expected critical IDs, verify p95 ≤ 5 seconds, and exercise priority and mixed-fault recovery with the assessed configuration. |
| Observability | Prometheus/Grafana/Loki; bounded OTel export and Tempo; propagated HTTP/message trace context. | New-feature metrics/traces and workload sizing during acceptance. |
| Report/submission | [M2 report](report-m2/IF4031_M2_DikejarDeadline.pdf) and [verification guide](m2-verification.md). | Course assessment and demo evidence. |

## Operational boundaries

- The verified four-VM setup runs on Refki's laptop (32 GiB RAM, 12 logical CPUs); teammates may use different machines. Worker loss is covered; physical-host/control-plane loss is outside this development topology's guarantee.
- Canonical writes and reads are per unit. Multi-unit delivery can partially commit and resume through receipts; there is no global transaction or cross-unit snapshot. Compare full checksums on a quiescent dataset.
- Partial pages have no continuation cursor; restart the scan after recovery. An unavailable unit must not be interpreted as an empty unit.
- Raw intake allows at most 10,000 unfinished records per source. HTTP bodies and normalized events have separate size limits. Completed raw inputs and history have no automatic cleanup; measure disk use with the official workload.
- A committed normalized plan is immutable during delivery retries. Parser fixes apply to inputs which failed before a plan committed. Processor state belongs in the supplied transaction, rather than a process-local cache.
- A local development Audit token is generated as a Secret. Replace it with the assistant-issued token for assessment. Supply the assistant-issued source tokens for the live satellite and coastal runs.
