# Milestone 2 requirements checklist

**Updated:** 20 November 2026, integrated development cluster and live satellite smoke check.

The integrated baseline includes the BMKG catalog importer, PVMBG schema modes and replay, satellite recognition, coastal alerts, Field Reports and Ops, and browser analytics. The full Go suite, vet, disposable PostgreSQL tests, and a synthetic end-to-end run passed. The [status summary](m2-status.md) lists the remaining setup and acceptance work.

| Status | Meaning |
|---|---|
| Done | Implemented and verified for the stated scope. |
| Done (fixtures) | Verified with controlled inputs. Any live or official evidence still required is listed in the row. |
| In progress | Partial implementation, prototype, or incomplete acceptance evidence. |
| Not done | Required implementation or artifact is missing. |
| Unverified | External action or condition cannot be confirmed from local files. |

IDs follow the specification's numbered criteria. Letter suffixes split compound criteria. Prototypes do not count as completed multi-node tests. Report statuses refer to an M2 report, not reusable M1 material.

[Platform](#platform-and-shared-requirements) · [P1](#p1-yang-pulang-membawa-laptopnya) · [P2](#p2-dua-nakhoda-satu-kapal) · [P3](#p3-surat-dari-tetangga) · [P4](#p4-atap-seng-di-siang-bolong) · [P5](#p5-pelampung-yang-berbohong) · [Audit](#audit-api-appendix-a) · [Report](#report) · [Demo/submission](#demonstration-and-submission) · [Bonus](#bonus)

## Platform and shared requirements

### Inherited M1 implementation rules

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| M1.1 | Communicate across services through network calls; share no business logic. | Done | HTTP and NATS boundaries retained. |
| M1.2 | Give each store one owning service; use its API externally. | Done | Aggregator owns canonical PostgreSQL. |
| M1.3a | Package each service separately, with its own Dockerfile. | Done | Eight application Dockerfiles, including the Field Team receiver. |
| M1.3b | Start the complete system through one orchestration command. | In progress | The integrated development profile passed on four VMs. Official inputs and the assessment profile still need a complete run. |
| M1.4 | Start/stop services independently and demonstrate real faults. | In progress | Independent service rollout and receiver replacement/worker-freeze checks exist; final M2 scenarios remain pending. |
| M1.5 | Keep BMKG and PVMBG credentials mutually invalid. | Done | Live credential-isolation check passed. |
| M1.6a | Load credentials/configuration externally; commit no active secrets. | Done | Generated private configuration and Secrets. |
| M1.6b | Provide `.env.example` with configuration names and examples. | Done | Present for implemented options. |
| M1.7a | Give every application service a health endpoint. | Done | Existing service readiness verified. |
| M1.7b | Propagate correlation IDs through structured logs. | Done | Existing API/Aggregator flow checked. |
| M1.7c | Record outgoing-call durations. | Done | Existing HTTP/broker/store instrumentation. |

### Kubernetes and deployment

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| K1 | Run all BNPB components on Kubernetes. | Done (fixtures) | Integrated rollout and baseline checks passed on the four-node practice cluster. |
| K2 | Use at least two nodes with separate kernels. | Done | Four separate Linux VM kernels; live placement checked. |
| K3 | Use separate-kernel nodes for the assessed demo. | In progress | Four separate-kernel VMs verified on Refki's laptop; final assessed deployment still needs validation. |
| K4 | Declare control-plane/worker roles and permitted node-failure targets. | Done | Control-plane/worker roles and worker-only failure targets documented. |
| K5 | Document one command deploying the complete system. | In progress | The k3s guide documents the orchestration command. Official catalog and assessment configuration still require a verified setup. |
| K6 | Set memory requests/limits according to Appendix B. | Done | Current pod templates match; values below. |
| K7a | Put credentials in Secrets and ordinary settings in ConfigMaps. | Done | Implemented for existing services. |
| K7b | Configure the new source/audit credentials securely. | In progress | Manifests reference mounted or injected Audit, satellite, and coastal credentials. Verify the official credentials on the assessment cluster. |
| K8 | Technically block non-owners from accessing another service's store. | Done | API-to-PostgreSQL denial verified. |
| TOPOLOGY | Declare node count/type, physical hosts, and host CPU/RAM in README. | Done | Root README documents the reference setup on Refki's laptop: four VMs, 12 logical CPUs, and 32 GiB RAM. Teammates may use different machines. |

### Memory requirements: Appendix B

These values are verified in the current templates. New components must retain the same class limits.

| Class | Request | Limit | Status |
|---|---|---|---|
| Aggregator | 256Mi | 512Mi | Done |
| Storage instance | 512Mi | 1Gi | Done |
| Broker / stream processor | 512Mi | 1Gi | Done |
| Consumer / event processor | 128Mi | 256Mi | Done |
| Auth / Client API | 128Mi | 256Mi | Done |
| BMKG / PVMBG mock | 64Mi | 128Mi | Done |
| Other components | 128Mi | 256Mi | Done |

CPU limits are optional. Load-testing tools are excluded from this table.

### Observability

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| O1 | Show service latency, error rate, and throughput in one dashboard. | In progress | Core, Field Team, and satellite metrics and panels exist. Workload sizing and integrated assessment evidence remain pending. |
| O2 | Trace a request across every participating service, including step durations. | In progress | HTTP/database and async source-to-consumer traces verified; new-feature traces pending. |
| O3 | Show structured, correlated logs for the complete system. | In progress | M1 flow verified; new ingestion flow pending. |

### Tester Agent

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| T1 | Install the prescribed, unchanged image as one agent per node. | Not done | No installation or agent readiness evidence for the integrated assessment cluster. |
| T2a | Allow its required privileges, RBAC, and scheduling. | Not done | Installation/permission validation pending. |
| T2b | Do not stop, replace, or obstruct the agent. | In progress | Namespace ingress allowed; full exercise pending. |
| T4 | Show the agent's action logs during tests. | Not done | No agent test evidence. |
| T5a | List and run the provided practice scenarios. | Not done | Practice scenarios not run. |
| T5b | Run encrypted exam scenarios with the assistant's key. | Not done | Exam execution pending. |
| T6a | Put application components in `sistem-informasi-kebencanaan-bnpb`. | Done | Current manifests use this namespace. |
| T6b | Label every application pod with an allowed `role`. | Done | Current templates use prescribed roles. |
| T7 | Keep `role=store` data on node-local disk. | Done | Current PostgreSQL uses local-path storage. |
| T8 | Demonstrate external faults, including VM freeze, when requested. | Done | Real worker VM suspension/resume checked; combined Tester Agent scenarios pending. |

### Updated contracts

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| C1 | Add `WILDFIRE` and `COASTAL` hazard types. | Done | Go types, schemas, fixtures. |
| C2 | Add `SATELIT` and `SENSOR_PANTAI` sources. | Done | Go types, schemas, fixtures. |
| C3 | Preserve BMKG/PVMBG contracts except specified admin extensions. | Done | Existing mocks retained. |
| C4 | Give Tim Lapangan the `POST /field-reports` write path. | Done | Client API route with `reports:write`; see [field reports](m2-field-reports.md). |
| C5 | Integrate the assistant's satellite feed and coastal sensor. | In progress | Both adapters are wired and passed together in a loopback fixture. Pinned sensor practice ran separately. Official satellite and exam sensor runs remain pending. |

## P1: Yang Pulang Membawa Laptopnya

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| P1.1a | Load the assistant's historical earthquake dataset. | In progress | Resumable import passed with a synthetic CSV. Deployment can package the same immutable file for all Aggregators. Official load and Audit comparison remain pending. |
| P1.1b | Ensure no node stores the entire canonical dataset. | Done | Four units with three copies each; each node is excluded from one unit. |
| P1.1c | Spread storage and writes across multiple nodes. | Done | Live canonical data and primary writes span four logical units on separate nodes. |
| P1.1d | Report data distribution per node. | In progress | Current per-unit counts/physical nodes reported by check-cluster; official-dataset measurements pending. |
| P1.2a | Handle the dataset's natural geographic skew. | In progress | Identity hashing tested with same-region fixtures. |
| P1.2b | Measure per-node load during dataset ingestion. | Not done | Official dataset/node measurements absent. |
| P1.2c | Measure per-node load during a regional earthquake burst. | Not done | Burst experiment absent. |
| P1.3a | Read across every storage unit through one request. | Done | Runtime fan-out reads all four primary Services. |
| P1.3b | Return correctly ordered, paginated results. | Done | Cross-unit keyset ordering and pagination verified against PostgreSQL. |
| P1.4a | Kill a worker and erase all its data. | Not done | Destructive node test absent. |
| P1.4b | Prove zero lost hazards using the assistant's Audit checksum. | In progress | Audit checksum implemented and matched to a quiescent live scan; official dataset/disk-loss comparison pending. |
| P1.4c | Return complete results or explicitly marked partial results. | In progress | Explicit partial reads verified during worker freeze and locked-unit tests; disk-erasure scenario pending. |
| P1.4d | Avoid HTTP 500 and hanging requests during recovery. | In progress | Development worker freeze retained all baseline IDs with zero HTTP errors; full destructive test pending. |
| P1.5a | Add a new or empty replacement node. | In progress | VM join/placement scripts exist; empty-disk replacement not exercised. |
| P1.5b | Make the new node carry data and workload. | In progress | Existing replicas rewind/rebuild automatically; empty replacement measurements pending. |
| P1.5c | Keep reads and writes running during node addition. | Not done | Live capacity-change test absent. |
| P1.6 | Restore tolerance for another node loss after recovery. | In progress | All twelve replicas restored after worker resume; empty-disk replacement acceptance pending. |
| P1.7a | Provide a separate analytics dashboard covering all five hazard categories. | Done | The embedded dashboard presents Gempa, Tsunami, Gunung api, Kebakaran hutan, and Pesisir. |
| P1.7b | Show event-count time series with a selectable time range. | Done | Occurrence-time line chart with selectable range. |
| P1.7c | Show counts by severity. | Done | Severity totals across active records. |
| P1.7d | Show counts by `area_name`. | Done | Area totals in the analytics page. |
| P1.7e | Match analytics counts to cross-unit Audit results. | In progress | An integrated fixture matched Audit across all five categories, plus severity, area, and the unique total. Official dataset comparison pending. |
| P1.7f | Keep analytics visible and explicitly partial during node loss. | In progress | Disposable PostgreSQL check verified missing-unit metadata. Live node-loss evidence pending. |

## P2: Dua Nakhoda, Satu Kapal

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| P2.1a | Run at least three Aggregators across different nodes behind one entry point. | Done | Three Aggregators on separate workers behind Client API. |
| P2.1b | Stop routing requests to unhealthy instances. | Done | Distinct-peer read failover and failed-peer suppression; development freeze checked. |
| P2.2a | Allow only one active fetcher per source. | In progress | All configured source adapters run under exclusive PostgreSQL sessions. Verify single ownership for every live source on the integrated cluster. |
| P2.2b | Prevent duplicate canonical hazards. | Done | Canonical identities, input receipts, and plan sequencing prevent repeated effects in current flow. |
| P2.2c | Prove upstream requests are not multiplied across instances. | In progress | Core BMKG/PVMBG upstream logs verified before feature integration. Repeat on the assessed configuration and during scaling. |
| P2.3 | Resume fetching within 60 seconds after owner death, without gaps or duplicates. | In progress | Live takeover/recovery exercised; formal source-gap timing still needed. |
| P2.4 | Prevent competing successful owners under clock shifts, including +45s/−30s. | In progress | +45/-30-second experiment passed with unchanged ownership; repeat on final feature-complete flow. |
| P2.5 | Reject a resumed frozen owner's writes until ownership is refreshed. | In progress | Stale-generation writes rejected and VM freeze exercised; final combined-flow evidence pending. |
| P2.6a | Sustain at least 10,000 VU for at least 120 seconds. | Not done | M1 load tests do not satisfy this. |
| P2.6b | Scale Aggregators 3 → 5 → 2 during the load test. | Not done | |
| P2.6c | Keep errors below 1%, excluding controlled rejections. | Not done | No qualifying M2 measurement. |
| P2.6d | Preserve single-source fetching throughout scaling. | Not done | |
| P2.7 | Achieve at least 2× single-instance throughput with three instances. | Not done | |
| P2.8 | Roll out a new Aggregator version under load without failed requests. | Not done | |

## P3: Surat dari Tetangga

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| P3.admin-a | Add a PVMBG field-renaming schema mode. | Done | Mock mode `rename_field`. |
| P3.admin-b | Add a PVMBG field-type-change mode. | Done | Mock mode `type_change`. |
| P3.admin-c | Add a PVMBG missing-required-field mode. | Done | Mock mode `missing_required_field`. |
| P3.1 | Declare and demonstrate automatically accepted versus rejected schema changes. | In progress | Three incompatible modes and opt-in adaptations verified locally. Live demo pending. |
| P3.2a | Preserve unprocessable bytes, source, and receipt time. | In progress | Admitted bytes and cursors commit atomically; full malformed/oversized HTTP-source demonstrations pending. |
| P3.2b | Retain each processing failure's reason. | Done | Failure reason/status/attempts retained with input bytes and metadata. |
| P3.2c | Classify retryable failures versus failures requiring a system change. | Done | Permanent mapping/input errors park; transient canonical failures retry with backoff. |
| P3.2d | Retry transient failures; park permanent failures. | Done | Retry/park lifecycle integrated with durable queue and replay. |
| P3.3a | Handle truncated JSON without crashes or restart loops. | In progress | Truncated-body byte preservation and safe parser rejection tested; live source-mode demo pending. |
| P3.3b | Handle excessively nested JSON safely. | In progress | Depth guard tested; full HTTP-source chaos demonstration pending. |
| P3.3c | Handle invalid UTF-8 safely and preserve failed input. | In progress | Invalid UTF-8 preserved and rejected safely in tests; live demonstration pending. |
| P3.3d | Handle oversized messages safely. | In progress | Body admission and normalized-envelope guards implemented; complete source experiment pending. |
| P3.3e | Keep processing other messages when bad messages arrive. | Done | PostgreSQL test proves an unrelated entity continues behind a parked input. |
| P3.4a | Replay failed messages after adapting the parser. | In progress | Explicit adaptation and ordered replay passed on PostgreSQL. Live demo pending. |
| P3.4b | Recover replayed messages without duplicate effects. | Done | Crash-after-commit and cross-unit retry protected by canonical receipts. |
| P3.4c | Preserve processing order per `source_ref_id`. | Done | OrderingKey blocks later entity versions; saved-plan sequences protect delayed delivery. |
| P3.5 | Allow failed-message inspection only to BNPB Internal Ops. | Done (fixtures) | `/failed-messages` requires `ops:failed-messages`, granted only to `bnpb-internal`. Long journal keys can still produce IDs rejected by the public route. |
| P3.6a | Return a fast 202 from `POST /field-reports`. | Done (fixtures) | 202 after one synchronous-replicated insert; latency measurement pending. |
| P3.6b | Accept reports while downstream processing is slow or stopped. | Done (fixtures) | Processor is decoupled; `FIELD_REPORT_PROCESSING_ENABLED=false` demonstrates it. |
| P3.6c | Lose no accepted report after receiver death. | Done (fixtures) | Receipt commits before 202; restart test in `sharded/fieldreport_integration_test.go`; live pod-kill demo pending. |
| P3.6d | Process accepted reports without duplicate effects. | Done (fixtures) | Idempotency-Key per client, `SKIP LOCKED` claim, guarded status transition; concurrent-worker and crash tests. |

## P4: Atap Seng di Siang Bolong

### Satellite interface

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| SAT.1 | Connect to `satellite.naufarrel.dev`. | Done | The deployed Aggregator connected and processed practice data. A bounded direct stream probe also resumed at the requested offset. |
| SAT.2 | Authenticate with the group bearer token stored as a Secret. | Done | Live practice requests authenticated successfully. Kubernetes deployment reads the group credential through a Secret. |
| SAT.3 | Read session state from `GET /v1/session`. | Done | Practice endpoint confirmed the 60× profile. The source owner polls it for the lag measurement. |
| SAT.4 | Consume SSE from `GET /v1/stream?from=<offset>`. | Done (fixtures) | The combined fixture read from offset zero into a fresh journal and reached both consumers. |
| SAT.5 | Handle `detection`, `update`, `retract`, and `heartbeat`. | Done (fixtures) | All four event types covered by adapter and recognizer tests. |
| SAT.6 | Resume using offsets or `Last-Event-ID`, preserving feed order. | Done (fixtures) | Resume checks use the committed next offset. |
| SAT.7 | Handle 400, 401, 429, and 503 feed responses. | Done (fixtures) | HTTP status classification and backoff tested. |

### Fire definition and canonical mapping

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| FIRE.1 | Deduplicate detections by satellite, date, time, latitude, and longitude. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.2 | Build UTC detection time from `acq_date` and `acq_time`. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.3 | Exclude low-confidence detections. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.4a | Connect detections within 1.0 km. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.4b | Require detection-time separation of at most 360 minutes. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.4c | Apply transitive connectivity across detections. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.5a | Treat connected groups of at least two detections as one fire. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.5b | Merge detections from different satellites into the same fire. | Done (fixtures) | Satellite recognizer tests. |
| FIRE.5c | Exclude singleton fires but report singleton counts. | In progress | Recognition excludes singletons. Produce a reproducible singleton count for the official run. |
| MAP.1 | Emit fire hazards as `SATELIT` / `WILDFIRE`. | Done (fixtures) | Satellite recognizer tests. |
| MAP.2 | Generate stable fire reference IDs across reprocessing. | Done (fixtures) | Satellite recognizer tests. |
| MAP.3 | Assign the province nearest the fire's center using a static reference table. | In progress | Nearest-province lookup uses a placeholder table. Confirm the reference coordinates. |
| MAP.4 | Store the fire center as latitude/longitude. | Done (fixtures) | Satellite recognizer tests. |
| MAP.5 | Set `occurred_at` to the earliest detection time. | Done (fixtures) | Satellite recognizer tests. |
| MAP.6a | Set AWAS for ≥20 detections or total FRP ≥100 MW. | Done (fixtures) | Satellite recognizer tests. |
| MAP.6b | Otherwise set SIAGA for ≥6 detections or total FRP ≥30 MW. | Done (fixtures) | Satellite recognizer tests. |
| MAP.6c | Otherwise set WASPADA. | Done (fixtures) | Satellite recognizer tests. |
| MAP.7 | Include count, satellites, highest confidence, total FRP, and detection keys. | Done (fixtures) | Full attributes tested, including 1,800 detection keys in the replicated profile. |

### Acceptance criteria

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| P4.1 | Produce one correct canonical hazard per fire. | In progress | Synthetic clustering and 1,800-key event verified through storage and delivery. Assistant reference pending. |
| P4.2a | Group by detection time, not BNPB arrival time. | Done (fixtures) | Connectivity uses UTC detection times. |
| P4.2b | Declare the accepted lateness bound. | Done | Default `SATELLITE_ALLOWED_LATENESS=12h` with per-satellite event-time watermarks. |
| P4.2c | Apply late detections within that bound to the existing fire. | Done (fixtures) | Late data updates the existing fire ID across different batch boundaries. |
| P4.2d | Report arrivals beyond the bound; never silently discard them. | In progress | Late detections are retained and counted. Official measurement pending. |
| P4.3 | Apply updates/retractions, including disappearing fires, without new duplicate events. | Done (fixtures) | Update, merge, split, and disappearance scenarios verify final fire membership and identity. |
| P4.4a | Lose no feed data at 60× speed. | Not done | A 1,800-detection synthetic fire passed. End-to-end loss at 60× and the maximum official event size remain unmeasured. |
| P4.4b | Catch up within 60 seconds after a burst ends. | In progress | Fixture queues drained. Live feed lag fell by about 11,000 offsets during a short smoke check, with over 1.3 million remaining. A timed burst run is pending. |
| P4.4c | Report p95 intake-to-canonical-storage latency. | In progress | Fixture verified samples from a batch's first input to canonical commit. Record p95 during the official run. |
| P4.4d | Show processing lag during bursts in a dashboard. | In progress | Grafana panels use the measured feed-to-journal lag and unfinished batches. Capture them during the official burst. |
| P4.5 | Recover interrupted recognition to the same final result, without loss or duplicates. | In progress | Saved-plan reuse, timeout recovery, and regrouping across batch boundaries passed fixtures. Live restart comparison pending. |
| P4.6 | Compare the full result with the assistant's reference; explain differences. | Not done | |
| P4.7 | Demonstrate several minutes of live-feed processing. | In progress | Deployed Aggregator processed live practice batches and published metrics. The multi-minute assessed demo remains pending. |
| P4.8 | Reconnect and resume without missing or duplicating data. | In progress | SSE reconnect and resume passed with simulated interruption. Live verification pending. |

## P5: Pelampung yang Berbohong

### Sensor setup and provided HTTP contract

The HTTP rows below use the separately supplied coastal-sensor contract. Example values do not define validation or severity thresholds.

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| SENSOR.1 | Run the assistant's pinned sensor image inside the cluster. | Done | Pinned image running on Refki’s development cluster. |
| SENSOR.2 | Keep the image unchanged; do not substitute its implementation. | Done | Manifest uses the supplied image and exact digest. |
| SENSOR.3 | Store `PANTAI_AUTH_TOKEN` as a Secret. | Done | Sensor and Aggregator reference the pantai-token Secret. |
| SENSOR.4 | Apply the provided practice/exam configuration and exam key when supplied. | In progress | Practice deployed. Exam key and profile still need assessment. |
| SENSOR.5 | Use bearer authentication and handle invalid-token 401 responses. | Done | Bearer auth, no credential redirects, and backoff for unsuccessful responses. |
| SENSOR.6 | Use unauthenticated `GET /healthz` for readiness. | Done | Kubernetes readiness uses /healthz. |
| SENSOR.7 | Read station IDs, names, provinces, and coordinates from `/v1/stations`. | Done | Adapter loads the station catalog and records its metadata for mapping. |
| SENSOR.8 | Read observations with `station_id`, `limit`, and `cursor` parameters. | Done | Adapter requests all stations with limit and opaque cursor. |
| SENSOR.9 | Respect ordering by `observed_at`, then `observation_id`. | Done | Observations are journaled in the order returned by the feed. |
| SENSOR.10 | Parse observation identity, time, measurements, quality, and `critical`. | Done | Typed validation and fixture checks. |
| SENSOR.11 | Resume observation pages using `next_cursor`. | Done | Cursor and raw inputs commit together. Fixture checks resume from an opaque cursor. |
| SENSOR.12 | Map COASTAL hazards according to the base sensor contract. | Done | Valid sensor observations appeared as COASTAL hazards in Client API. |
| SENSOR.13 | Avoid assumptions tied to practice IDs, seeds, order, or patterns. | In progress | Adapter uses contract fields and arbitrary fixture IDs. Exam scenarios remain pending. |

### Acceptance criteria

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| P5.1a | Deliver every critical event to the actual Field Team notification receiver. | In progress | Fixture delivered the critical observation after catalog and receiver recovery. The evidence checker scans all receipt pages and compares them with an independent expected-ID list. |
| P5.1b | Achieve zero critical-event loss and p95 delivery latency ≤5 seconds. | In progress | The checker uses independent wall-clock availability timestamps for end-to-end p95. Run it against the complete expected critical set during assessment. |
| P5.2 | Quarantine invalid sensor data with reasons instead of storing canonical hazards. | In progress | Invalid observations park with a reason. Valid inputs retry while station metadata is missing. Exam-profile fault evidence pending. |
| P5.3 | Preserve M1 latency for other sources during sensor faults. | Not done | No sensor-fault load test. |
| P5.4a | Avoid request storms while the sensor is unhealthy. | In progress | Bounded request timeout and jittered backoff. Integrated request-rate measurement pending. |
| P5.4b | Answer dependent reads quickly using cached data or explicit unavailability. | In progress | During a practice sensor outage, cached coastal data returned with stale status. Load criteria remain pending. |
| P5.4c | Resume using the sensor automatically after recovery. | In progress | Practice sensor resumed ingestion from its committed cursor after restart. |
| P5.4d | Detect stale observations even when health returns success. | Done | Fixture detects stale observation time even when the sensor responds successfully. |
| P5.5a | Preserve Field Team service under overload. | Done (fixtures) | Reserved admission for Field Team traffic and a separate Field Report intake cap are implemented. Verify reads and writes during the integrated Media load test. |
| P5.5b | Reject excess Media requests in a controlled manner. | Done (fixtures) | Media limited to the general pool; excess gets 429 + `Retry-After`. |
| P5.6a | Survive unannounced combinations of source, node, network, and load failures. | Not done | Both delivery services use local volumes on the declared non-target control plane. Verify combined faults, including their pod or network interruptions, on the assessed cluster. |
| P5.6b | Recover without manual intervention during the recovery window. | Not done | Run the recovery window without manual intervention and compare accepted reports, source progress, and critical receipts afterward. |
| P5.7 | Let every member diagnose failures from dashboards, metrics, and traces. | In progress | Core metrics/logs/traces are available; team diagnosis and integrated fault exercises remain pending. |
| P5.8a | Define reliability targets for the Field Team warning path. | Not done | Record the Field Team alert SLO and measurement boundary before the game-day run. |
| P5.8b | Report reliability/error-budget consumption during game day. | Not done | Compare the game-day run with the declared alert SLO and quantify the budget consumed. |

## Audit API: Appendix A

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| A.auth | Expose Audit through Aggregator using the assistant-issued bearer token. | In progress | Audit bearer routes work with a local development token; assistant-issued token not supplied yet. |
| A.secret | Store the audit token as a Secret. | Done | Audit credential is a Kubernetes Secret injected only into Aggregator. |
| A.format | Use exact field names, JSON Content-Type, and UTC ISO-8601 timestamps. | Done | Typed JSON responses, UTC times, and HTTP format tests. |
| A.source | Support filtering one source or including all sources. | Done | Audit filters one source or includes all canonical sources. |
| A.page | Default to 100 items; cap at 1000; use opaque cursors and terminal null. | Done | Default100/max1000, scoped opaque cursors, final null; PostgreSQL pagination check. |
| A.partial | Return `partial` and `unavailable_units` instead of 500 for missing units. | Done | Bounded fan-out reports missing units and withholds continuation on partial pages. |
| A.errors | Match the specified error body for 400/401/403/404/429/503. | Done | JSON error shape for auth/query/not-found/admission/dependency responses. |
| A.events.1 | Implement `GET /internal/audit/events`, returning complete HazardEvents. | Done | Live event reads through Aggregator verified. |
| A.events.2 | Support `source`, `since`, `limit`, and `cursor`. | Done | Source/since/limit/cursor implemented. |
| A.events.3 | Interpret `since` as `ingested_at >= since`. | Done | Uses persisted ingestion timestamps and correct microsecond-boundary filtering. |
| A.events.4 | Order by `ingested_at ASC`, then `hazard_id ASC`. | Done | Ascending ingestion timestamp and hazard ID, independent of public occurrence ordering. |
| A.events.5 | Preserve original detection keys without coordinate rounding. | Not done | No feed-to-Audit path. |
| A.checksum.1 | Implement `GET /internal/audit/checksum` with source filtering and count. | Done | Live checksum/count and source filtering implemented. |
| A.checksum.2 | Sort all `source_ref_id` values by byte order. | Done | Go byte-lexicographic sorting of reference IDs. |
| A.checksum.3 | Join IDs with newline separators, without a trailing newline. | Done | Newline separators, no trailing newline; PostgreSQL expected-digest check. |
| A.checksum.4 | Return SHA-256 as 64 lowercase hexadecimal characters. | Done | SHA-256 encoded as lowercase hexadecimal. |
| A.checksum.5 | Include algorithm, timestamp, and completeness metadata. | Done | Algorithm/as_of/partial/unavailable_units included. |
| A.location.1 | Implement `GET /internal/audit/location`; require `hazard_id`. | Done | Location route validates required hazard_id. |
| A.location.2 | Return real Kubernetes node names, units, and primary/replica roles. | Done | Live Kubernetes discovery plus TLS SQL presence checks verified three copies on distinct nodes. |
| A.location.3 | Return 404 for an unknown hazard. | Done | Complete negative lookup returns404; uncertain lookup reports partial. |
| A.unprocessed.1 | Implement `GET /internal/audit/unprocessed` with filters and pagination. | Done | Source-filtered, paginated unprocessed journal API. |
| A.unprocessed.2 | Include ID, source, receipt time, reason, retryability, attempts, and status. | Done | Required metadata/status fields included; original bytes restricted to owner API. |
| A.unprocessed.3 | Use `retrying`/`parked`; exclude successfully recovered messages. | Done | Recovered messages disappear; retrying/parked lifecycle checked. |

## Report

No M2 report exists in this worktree. Design notes and M1 report material are preparation, not completed report sections.

### General sections and writing rules

| ID | Requirement | Status |
|---|---|---|
| R.general.1 | Include the cover, group/member identities, and contribution table. | Not done |
| R.general.2 | Describe the complete system and its scope. | Not done |
| R.general.3 | Include the system architecture diagram. | Not done |
| R.general.4 | List technologies and explain their selection. | Not done |
| R.general.5 | State assumptions and running instructions. | Not done |
| R.general.6 | Diagram component placement and cross-node data paths. | Not done |
| R.general.7 | Explain M1 decisions retained, changed, or discarded. | Not done |
| R.general.8 | Declare actual nodes, physical hosts, and host CPU/RAM. | Not done |
| R.general.9 | Include the required AI-use form. | Not done |
| R.common.1 | Identify the problem and its literature terminology for every P1–P5. | Not done |
| R.common.2 | Explain each problem's solution and mechanism. | Not done |
| R.common.3 | Compare alternatives and trade-offs. | Not done |
| R.common.4 | State deliberate limitations and their reasons. | Not done |
| R.common.5 | Provide evidence for every acceptance criterion. | Not done |
| R.common.6 | Answer every problem-specific “Di laporan” item. | Not done |
| R.writing.1 | Make every claim traceable to code or documented evidence. | Not done |
| R.writing.2 | Avoid copied report/code from other groups or internet projects. | Unverified |

### Problem-specific report answers

| ID | Required answer | Status |
|---|---|---|
| R.P1.1 | Name and explain the distributed-storage problems. | Not done |
| R.P1.2 | Explain placement and membership changes when nodes join/leave. | Not done |
| R.P1.3 | Compare at least two alternatives and their behavior under skew. | Not done |
| R.P1.4 | State each endpoint's consistency during normal operation and node loss. | Not done |
| R.P1.5 | Explain cross-unit analytics and incomplete-result indicators. | Not done |
| R.P2.1 | Explain duplicate ownership, clock skew, and resumed stale owners. | Not done |
| R.P2.2 | Explain time authority and safety under skew/freeze. | Not done |
| R.P2.3 | Explain ownership-loss timeouts and their trade-offs. | Not done |
| R.P2.4 | Include load-test tooling and configuration. | Not done |
| R.P3.1 | Explain invalid-message handling, replay, ordering, and durable acceptance. | Not done |
| R.P3.2 | Explain schema-evolution policy and its rationale. | Not done |
| R.P3.3 | Explain failed-message storage, inspection, replay, and deduplication. | Not done |
| R.P3.4 | State and prove Field Team report delivery guarantees. | Not done |
| R.P4.1 | Explain event time, burst processing, and state recovery concepts. | Not done |
| R.P4.2 | Explain retaining/replacing M1 polling for this source. | Not done |
| R.P4.3 | Explain finalization, lateness, and durable recognition state. | Not done |
| R.P4.4 | State and prove processing guarantees. | Not done |
| R.P5.1 | Identify the problems behind criteria 2–5. | Not done |
| R.P5.2 | Explain resilience mechanisms, configuration values, and rationale. | Not done |
| R.P5.3 | Document discovered sensor faults, discovery methods, and handling. | Not done |
| R.P5.4 | Write the game-day post-mortem: detection, impact, mitigation, improvements. | Not done |

## Demonstration and submission

| ID | Requirement | Status | Current evidence / gap |
|---|---|---|---|
| D1 | Submit complete code, manifests, updated mocks, `.env.example`, and README. | In progress | M2 functionality still incomplete. |
| D2 | Generate `IF4031_M2_DikejarDeadline.pdf`. | Not done | M1 PDFs only. |
| D3 | Save auditor results for exam scenarios before the demo. | Not done | |
| README.1 | Document startup from a fresh environment. | In progress | M1 guide and development VM instructions only. |
| README.2 | Document the deployed M2 topology and configuration variables. | In progress | Reference topology and implemented configuration documented; new-feature options remain pending. |
| README.3 | Document how to trigger every required test scenario. | In progress | M1 scenarios and protocol checks only. |
| V1 | Record architecture, code walkthrough, and complete testing. | Not done | No M2 video evidence. |
| V2 | Keep video within the stated 30–45 minute maximum. | Not done | |
| V3 | Show cURL/Postman requests with `time_total`. | Not done | |
| V4 | Show analytics/metrics dashboards and a database CLI/GUI. | Not done | |
| V5 | Show Tester Agent logs while exam scenarios run. | Not done | |
| V6 | Demonstrate P1–P5, including loss, failover, replay, satellite, and sensor faults. | Not done | |
| V7 | Have every member appear and explain their architecture/code contribution. | Not done | |
| V8 | Request a fresh exam token before recording. | Unverified | Assistant coordination is external. |
| V9 | Start the satellite exam with `POST /v1/exam/start` when recording begins. | Not done | |
| V10 | Use supplied exam keys, Audit credentials, and reference/load-test resources. | Not done | |
| V11 | Record within the assigned post-resource testing window. | Not done | The specification's 24–48 hours is illustrative. |
| V12 | Upload an HTTPS Unlisted YouTube or Google Drive video link. | Unverified | External upload not inspected. |
| V13 | Submit `POST /v1/video` with group bearer token and `{"url":"https://..."}`. | Unverified | External submission not inspected. |
| V14 | Save the 200 response containing `received_at`. | Unverified | |
| V15 | Verify the final submitted link with `GET /v1/video`. | Unverified | Last submission before deadline is assessed. |
| S1 | Use the M1 repository or a replacement meeting the same visibility rules. | Done | Same repository retained. |
| S2 | Tag the assessed commit `milestone-2` and create its release. | Unverified | Only `milestone-1` is present locally. |
| S3 | Submit the group form once, including repository/report links. | Unverified | External submission not inspected. |
| S4 | Submit code/report by **20 Nov 2026, 23:59 WIB**. | Not done | Final M2 artifacts are incomplete. |
| S5 | Freeze the assessed version by the code deadline. | Not done | Later commits are not assessed. |
| S6 | Submit the video by **27 Nov 2026, 23:59 WIB**. | Unverified | External submission not inspected. |

## Bonus

Optional. Each bonus is worth 5 points; total score is capped at 110.

| ID | Requirement | Status | Evidence required |
|---|---|---|---|
| B1 | Run nodes on different physical hosts. | Not done | Host views, real-network latency/lag, and cable/Wi-Fi disconnection. |
| B2 | Add an AI/ML component without disrupting warnings when slow, wrong, or stopped. | Not done | Implemented component and isolation demonstration. |
| B3 | Scale instance count automatically as load rises and falls. | Not done | Automatic scale-up and scale-down under load. |
| B4 | Implement and explain every claimed bonus. | Not done | Code, report, and supporting evidence. |

## Basis and verification

- Course specification `IF4031 - Tugas Besar - M2.pdf`, including Appendices A/B and inherited M1 rules; coastal-sensor contract/deployment example supplied separately.
- [Deployment manifests](../infra/k8s/cluster/), [shared contracts](../contracts/), and [foundation design](m2-foundation.md).
- `python3 scripts/m2/check-foundation.py`: disposable PostgreSQL protocol checks.
- `python3 scripts/m2/check-pipeline.py --exercise-recovery`: live durable-intake recovery and quiescent checksum comparison.
- `python3 scripts/m2/check-cluster.py`: current deployment, replication, and observability checks.

Results are written under ignored `.state/m2/`. The verified development topology runs on Refki's laptop; teammates may use different machines. Exam scenarios, final workload results, and external submissions require their own recorded evidence.
