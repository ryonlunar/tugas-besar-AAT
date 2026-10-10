# PVMBG Mock

Menyediakan laporan vulkanik sintetis untuk simulasi, dengan kontrol delay, outage, dan perubahan skema saat service berjalan. Data ini bukan data PVMBG produksi.

## Menjalankan

Memerlukan Go 1.23 atau lebih baru. Jalankan dari root repo:

```sh
PVMBG_TOKEN='local-demo-token' go run ./services/pvmbg-mock/cmd/server
```

Port default 8082, dengan delay acak 500–3000 ms. Katalog awal berisi 20 laporan dan menambah satu laporan setiap 10 detik. Konfigurasi tersedia di [.env.example](../../.env.example).

## Endpoint

Endpoint data dan admin memerlukan `Authorization: Bearer <PVMBG_TOKEN>`:

| Endpoint | Fungsi / body JSON |
|---|---|
| `GET /volcanic-reports` | Daftar laporan; parameter opsional `since` dalam format RFC3339 |
| `POST /admin/schema-version` | `{"version":2}` mengaktifkan `confidence_level`; versi 1 menghilangkannya |
| `POST /admin/delay` | `{"min_ms":3000,"max_ms":3000}` mengatur delay respons |
| `POST /admin/outage` | `{"enabled":true}` mengaktifkan outage; `false` memulihkan service |
| `GET /health` | Status proses; tetap 200 saat outage |
| `GET /ready` | Kesiapan service; gagal saat outage |

Saat outage, endpoint data mengembalikan 503. Perubahan skema berlaku pada seluruh laporan tanpa mengganti ID atau merestart service.

```sh
curl -H 'Authorization: Bearer local-demo-token' http://localhost:8082/volcanic-reports
curl -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer local-demo-token' \
  -H 'Content-Type: application/json' -d '{"version":2}'
```

## Pengujian

```sh
go test ./services/pvmbg-mock/...
```
