# M2 Field Reports, Ops access, and request priority

Covers P3 criteria 5–6 and P5 criterion 5. See the [foundation design](m2-foundation.md) for shared storage and the [checklist](m2-requirements-checklist.md) for status.

## Permissions

Auth assigns scopes per client on the server. A login request cannot choose them. Client API rejects a token whose scopes exceed its subject's allowance with 401, and a token that lacks the route's scope with 403.

| Client | Scopes | New routes |
|---|---|---|
| `media` | `hazards:summary` | none |
| `field-team` | `hazards:summary hazards:raw reports:write` | `POST /field-reports`, `GET /field-reports/{report_id}` |
| `bnpb-internal` | `hazards:summary hazards:raw ops:failed-messages` | `GET /failed-messages`, `GET /failed-messages/{message_id}`, `POST /failed-messages/{message_id}/replay` |

The failed-message routes forward to Aggregator's internal owner API. The Audit token still grants neither raw inspection nor replay. The inspection response carries the original payload and auxiliary bytes in base64, so it can exceed 1 MiB for payloads the journal retains (`SOURCE_RESPONSE_MAX_BYTES`). Client API therefore streams it without its buffered response limit, and `AGGREGATOR_REQUEST_TIMEOUT` bounds the transfer.

## Field Report intake

```
Tim Lapangan ──POST /field-reports──► Client API ──► Aggregator ──INSERT──► unit primary ⇄ sync standby
                Idempotency-Key       validate,      validate,     (bytes, hash, receipt)
                                      scope, priority own slots                 │ 202 {report_id}
                                                                                ▼
                                             field-report worker (every Aggregator, SKIP LOCKED)
                                             accepted → processed | rejected, in the claiming transaction
```

1. Client API verifies the token, the `reports:write` scope, the `Idempotency-Key` syntax (1–128 of `A-Z a-z 0-9 . _ : -`), the 32 KiB body limit, and `field-report.schema.json`. The description cannot contain U+0000, which JSON can escape but PostgreSQL `text` cannot store. Invalid requests never reach storage.
2. Aggregator repeats the validation and inserts one row holding the original bytes, their SHA-256, the client, the key, and a random report ID. `partition.ReportUnit(client, key)` picks the unit, so every retry of one request reaches the same receipt.
3. Patroni's strict synchronous mode requires a standby acknowledgement before commit returns. The response is `202 {"report_id":"…","status":"accepted"}` only after that commit.
4. A retry with the same key and the same bytes returns the first receipt with `202` and `Idempotent-Replayed: true`. The same key with different bytes returns `409 IDEMPOTENCY_CONFLICT`. Keys are scoped per client.
   A row visible on the primary does not prove that the standby has it. If the standby does not acknowledge in time, the first attempt gets `503`, yet PostgreSQL has already committed the row locally. So before returning the first receipt, a retry runs its own synchronous commit on that row (a no-op `UPDATE`). That commit completes only after the standby has flushed WAL past the original insert. Until then the retry also gets `503`, and the client keeps retrying with the same key.
5. A worker in each Aggregator claims accepted rows with `FOR UPDATE SKIP LOCKED`, applies one transition (`processed` with parsed fields, or `rejected` with a reason), and commits it with the claim. The `WHERE status='accepted'` guard prevents a second transition. A crash before commit releases the row for another worker.
   Each row runs inside its own savepoint. If PostgreSQL refuses a row's values (SQLSTATE class 22 or 23), only that row rolls back, and it is recorded as `rejected` with `storage_incompatible: sqlstate …`. The rest of the batch still commits, so one bad row cannot hold back valid reports on its unit. Any other error, such as a lost connection or a timeout, rolls back the whole batch for a retry, so an outage never rejects a valid report.

Acceptance needs only the owning storage unit. It does not depend on the broker or the processor. `FIELD_REPORT_PROCESSING_ENABLED=false` stops processing while intake continues. When storage cannot commit, the response is `503` with `Retry-After`, and the client retries with the same key.

**Delivery guarantee:** at-least-once delivery from the client (it retries until it gets a 202). Idempotent acceptance makes that effectively-once storage. Processing is exactly-once in effect: the outcome and the claim commit atomically, and a committed report is never claimed again. A 202 means the report is on two PostgreSQL instances on different nodes.

**Known limits:**
- A report whose home unit has no writable primary cannot be accepted until Patroni promotes a standby. The client gets a 503 rather than a false 202.
- The status route searches all four units because report IDs are random. If any unit is unavailable and the report is not found, it returns 503 instead of 404.
- Processing currently validates and records the report. Notifying responders from a report is not implemented.

## Request priority

Client API and Aggregator each split their in-flight slots into a general pool and a reserved quarter (at least one slot). Every caller can use the general pool. Only Tim Lapangan and BNPB Internal can use the reserve; Client API authenticates them and marks their forwarded requests `X-Request-Priority: high`. When the general pool is full, Media receives `429 RATE_LIMITED` with `Retry-After: 1` while priority requests are still admitted. Field Report intake also has its own Aggregator slots (`FIELD_REPORT_MAX_INFLIGHT`), so hazard reads cannot block report acceptance.

Authentication now runs before admission, because the caller's identity selects the pool.

## Configuration

| Variable | Default | Service | Purpose |
|---|---|---|---|
| `FIELD_REPORT_PROCESSING_ENABLED` | `true` | Aggregator | Run the report processor. `false` keeps intake running. |
| `FIELD_REPORT_PROCESS_INTERVAL` | `500ms` | Aggregator | Idle poll interval of the processor. |
| `FIELD_REPORT_MAX_INFLIGHT` | `64` | Aggregator | Concurrent intake requests per instance. |
| `DB_REPORT_MAX_CONNS` | `4` | Aggregator | Report connections per storage unit, separate from ingestion pools. |
| `API_MAX_INFLIGHT` / `AGGREGATOR_MAX_INFLIGHT` | `128` | both | Total admission slots; a quarter is reserved for priority callers. |

## Verification

Fixture and unit checks run anywhere:

```bash
go test ./contracts ./services/client-api/... ./services/aggregator/internal/fieldreport ./services/aggregator/internal/api
```

Storage checks run with the disposable foundation databases (`scripts/m2/check-foundation.py` sets `AAT_FOUNDATION_DSN_FILE`). They cover:

- idempotent retries and per-client key scope
- 16 concurrent duplicates storing one row
- receipts surviving a receiver restart
- three concurrent workers processing each report once
- a processor crash before commit
- an unstorable queued row rejected without blocking the valid reports behind it

Run them with:

```bash
go test -run TestFieldReport -v ./services/aggregator/internal/sharded
```

### Manual demo

Log in as each client following the README's [manual login](../README.md#login-manual) (`field-team`, `media`, `bnpb-internal`), then set `FIELD_ACCESS_TOKEN`, `MEDIA_ACCESS_TOKEN`, and `OPS_ACCESS_TOKEN`.

Submit a report, then retry it with the same key. Both responses carry the same `report_id`:

```bash
KEY="report-$(date +%s)"
for attempt in 1 2; do
  curl -sS -w ' %{http_code} %{time_total}s\n' -X POST "$API_URL/field-reports" \
    -H "Authorization: Bearer $FIELD_ACCESS_TOKEN" -H "Idempotency-Key: $KEY" \
    -H 'Content-Type: application/json' --data-binary @contracts/examples/field-report.json
done
```

Accept while processing is stopped, kill the receiving pods after the 202, then resume and confirm the status becomes `processed`:

```bash
kubectl -n sistem-informasi-kebencanaan-bnpb set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED=false
kubectl -n sistem-informasi-kebencanaan-bnpb rollout status deployment/aggregator
# POST a report as above and keep its report_id in REPORT_ID, then:
kubectl -n sistem-informasi-kebencanaan-bnpb delete pod -l app=aggregator --wait=false
kubectl -n sistem-informasi-kebencanaan-bnpb set env deployment/aggregator FIELD_REPORT_PROCESSING_ENABLED-
curl -sS "$API_URL/field-reports/$REPORT_ID" -H "Authorization: Bearer $FIELD_ACCESS_TOKEN"
```

Check access restrictions. Media and Tim Lapangan get 403; BNPB Internal gets the list:

```bash
for token in "$MEDIA_ACCESS_TOKEN" "$FIELD_ACCESS_TOKEN" "$OPS_ACCESS_TOKEN"; do
  curl -sS -o /dev/null -w '%{http_code}\n' "$API_URL/failed-messages?source=PVMBG" -H "Authorization: Bearer $token"
done
```

The `app=aggregator` selector and namespace follow the k3s manifests; adjust them if the deployment labels differ. Priority under overload is measured during the P5 load test: run a Media read flood and a Tim Lapangan stream together, and compare their 429 rates.
