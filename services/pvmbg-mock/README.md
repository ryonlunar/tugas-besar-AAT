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
| `POST /admin/schema-version` | `{"version":2}` mengaktifkan `confidence_level`; versi 1 menghilangkannya. Versi 3-5 memicu perubahan skema **tidak kompatibel** (lihat tabel di bawah); body menerima **tepat satu** dari `version` (1-5) atau alias bernama `mode` |
| `POST /admin/delay` | `{"min_ms":3000,"max_ms":3000}` mengatur delay respons |
| `POST /admin/outage` | `{"enabled":true}` mengaktifkan outage; `false` memulihkan service |
| `GET /health` | Status proses; tetap 200 saat outage |
| `GET /ready` | Kesiapan service; gagal saat outage |

Saat outage, endpoint data mengembalikan 503. Perubahan skema berlaku pada seluruh laporan tanpa mengganti ID atau merestart service.

### Mode skema (`PVMBG_SCHEMA_VERSION` / `POST /admin/schema-version`)

| Versi | Alias `mode` | Perubahan terhadap v2 |
|---|---|---|
| 1 | `compatible_v1` | Tanpa `confidence_level` (baseline M1) |
| 2 | `compatible_v2` | Menambah `confidence_level` (aditif, kompatibel) |
| 3 | `rename_field` | `alert_level` di-*rename* menjadi `level` (field wajib hilang dari sudut pandang konsumen v1/v2) |
| 4 | `type_change` | `eruption_count_24h` berubah dari number menjadi string desimal (nilai sama, mis. `3` -> `"3"`) |
| 5 | `missing_required_field` | `ash_column_height_m` dihilangkan dari respons |

Versi 3-5 **tidak kompatibel** secara sengaja — lihat `docs/m2-pvmbg-schema-policy.md` pada repo Aggregator untuk kebijakan accept/park dan cara mengaktifkan adaptasi setelah park.

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
