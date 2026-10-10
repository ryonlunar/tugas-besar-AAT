# Aggregator

Melakukan polling BMKG dan PVMBG secara independen, memetakan data menjadi `HazardEvent`, lalu menyimpan data dan riwayat revisi ke PostgreSQL. Event dikirim ke NATS JetStream melalui transactional outbox.

## Konfigurasi dan menjalankan

Untuk menjalankan seluruh dependensi, gunakan [Docker Compose di root repo](../../README.md#menjalankan-aplikasi).

Jika menjalankan binary langsung, sediakan PostgreSQL, NATS, dan kedua mock. Ekspor konfigurasi koneksi beserta `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_API_KEY`, `POSTGRES_PASSWORD`, dan `NATS_TOKEN` sesuai [.env.example](../../.env.example); sesuaikan alamat host dengan lokasi dependensi. Jalankan dari root repo:

```sh
go run ./services/aggregator/cmd/server
```

[Migration PostgreSQL](internal/store/migrations/) disertakan dalam binary dan diterapkan saat startup.

## Endpoint

| Endpoint | Fungsi |
|---|---|
| `GET /internal/v1/hazards` | Daftar hazard; parameter `source=BMKG\|PVMBG`, `view=summary\|full`, `limit=1..100`, dan `offset>=0` |
| `GET /internal/v1/hazards/{hazard_id}` | Detail hazard |
| `GET /health` | Status proses |
| `GET /ready` | Kesiapan koneksi PostgreSQL |

Endpoint `/internal/v1/` memerlukan header `X-Internal-Key` dengan nilai `AGGREGATOR_API_KEY`.

## Pengujian

```sh
go test ./services/aggregator/...
```

Tes integrasi PostgreSQL memerlukan `AAT_TEST_DATABASE_URL` dengan nama database berakhiran `_test` dan `AAT_TEST_ALLOW_DESTRUCTIVE_RESET=yes`. Tes mengosongkan tabel dengan `TRUNCATE ... CASCADE`; gunakan database khusus pengujian. Tes NATS memerlukan `AAT_TEST_NATS_URL` dan `AAT_TEST_NATS_TOKEN` yang menunjuk ke server khusus pengujian.
