# Client API

Memverifikasi JWT RS256 secara lokal dan membaca data melalui HTTP internal Aggregator.

## Konfigurasi

Memerlukan `JWT_PUBLIC_KEY_FILE`, `AGGREGATOR_BASE_URL`, dan `AGGREGATOR_API_KEY`. Setup service tersedia di [README utama](../../README.md#menjalankan-aplikasi).

## Endpoint dan hak akses

| Endpoint | Fungsi |
|---|---|
| `GET /v1/hazards` | Daftar hazard; parameter `source=BMKG\|PVMBG`, `view=summary\|full`, `limit`, dan `offset` |
| `GET /v1/hazards/{hazard_id}` | Detail hazard; parameter `view=summary\|full` |
| `GET /health` | Status proses |
| `GET /ready` | Kesiapan service |

Endpoint `/v1/hazards` dan detailnya memerlukan `Authorization: Bearer <access_token>`. Media hanya menerima tujuh field ringkasan; permintaan `view=full` atau field raw ditolak dengan 403. Client `field-team` dan `bnpb-internal` dapat meminta `view=full`.

## Pengujian

```sh
go test ./services/client-api/...
```
