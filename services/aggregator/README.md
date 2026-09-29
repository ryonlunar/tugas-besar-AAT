# Aggregator

Pemilik: Hazim. Ini adalah slice implementasi M1 untuk ingest, penyimpanan kanonik, internal read API, dan pengiriman event. Sumber dan lokasi gunung di mock adalah fixture sintetis; bukan data produksi.

## Yang tersedia

- `cmd/server`: runtime Aggregator, graceful HTTP shutdown, migrasi saat startup, health/readiness, polling dua sumber secara independen, dan loop relay outbox.
- `internal/poller`: klien HTTP BMKG/PVMBG dengan credential terpisah, deadline dan batas ukuran respons; UUID correlation per siklus diteruskan ke mock; polling full-snapshot, mapping record valid, dan status degraded untuk record invalid/oversized.
- `internal/store`: read pool terpisah dari writer BMKG, writer PVMBG, dan relay; migrasi checksum; ID hazard stabil; latest state + `hazard_versions` append-only; source freshness; transactional outbox.
- `internal/api`: `GET /health`, `GET /ready`, `GET /internal/v1/hazards`, dan detail per ID. Route internal memakai `X-Internal-Key`, ringkasan/full view, metadata freshness, correlation ID, dan admission limit.
- `internal/outbox` dan `internal/broker`: retry bounded dari waktu kegagalan publish atau persist ACK; throttle lokal per message dipakai bila metadata retry gagal ditulis (tidak bertahan melewati restart); publish timeout membatasi koneksi, pemeriksaan/penyiapan stream, dan publish; stream contract validation dan `Nats-Msg-Id` untuk deduplikasi broker.
- `Dockerfile`: image runtime non-root untuk service Aggregator; root Compose kini menghubungkan image ini dengan dua mock, PostgreSQL, dan NATS. Compose lengkap dengan Auth/Client API dan consumers tetap dikoordinasikan Refki.

## Menjalankan lokal

Sediakan PostgreSQL, NATS JetStream dengan token autentikasi, kedua mock, lalu set subset env milik Aggregator. Secret harus dibuat lokal; jangan commit atau menaruh credential aktif pada README atau `.env.example`.

Variabel wajib: `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_API_KEY`, `POSTGRES_PASSWORD`, dan `NATS_TOKEN`. Host/port/base URL dapat diarahkan ke Compose atau lokal. Jalankan:

```bash
go run ./services/aggregator/cmd/server
```

Proses memeriksa PostgreSQL, menerapkan migration dengan checksum, lalu mulai polling dan relay; pembacaan API hanya lewat jaringan Aggregator. Permintaan internal memerlukan header `X-Internal-Key` yang nilainya sama dengan `AGGREGATOR_API_KEY`.

`GET /internal/v1/hazards` menerima `source=BMKG|PVMBG`, `view=summary|full`, `limit` 1–100, `offset` >=0. Endpoint detail memakai `/internal/v1/hazards/{hazard_id}`. `/health` hanya memeriksa proses; `/ready` memeriksa koneksi Canonical Store.

## Verifikasi

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
```

Tes PostgreSQL integration hanya berjalan jika `AAT_TEST_DATABASE_URL` menunjuk ke database disposable yang namanya berakhiran `_test` dan `AAT_TEST_ALLOW_DESTRUCTIVE_RESET=yes` diberikan secara eksplisit. Tes mengosongkan tabel aplikasi dengan `TRUNCATE ... CASCADE`; jangan arahkan ke database bersama atau produksi. Tes JetStream integration berjalan jika `AAT_TEST_NATS_URL` dan `AAT_TEST_NATS_TOKEN` diarahkan ke server disposable.

Slice Docker Compose pernah dibangun dan dijalankan pada smoke historis 2026-09-28: health/readiness sehat, API internal mengembalikan hazard BMKG/PVMBG berstatus fresh, dan stream JetStream menerima event. Commit dan digest image yang diuji tidak direkam, jadi bukti itu tidak memverifikasi remediation saat ini. Ini bukan acceptance penuh P1/P4/P5. Auth/Client API milik Bimo, consumer dan Compose lengkap milik Refki, sustained load p95 (bukti P2 milik Bimo; perbaikan polling dapat melibatkan Hazim), serta broker/consumer crash-recovery masih perlu diuji sesuai pembagian kontrak.
