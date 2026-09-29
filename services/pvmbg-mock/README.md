# pvmbg-mock

Pemilik: Hazim. Mock PVMBG ini sudah dapat dijalankan sebagai service HTTP mandiri; isinya laporan vulkanik sintetis berlabel Tamriel, bukan data PVMBG produksi.

## Implementasi

- `GET /volcanic-reports?since=<RFC3339>` memakai `Authorization: Bearer <PVMBG_TOKEN>`.
- `POST /admin/schema-version`, `/admin/outage`, dan `/admin/delay` memakai credential Bearer yang sama. Body admin dibatasi 4 KiB dan field yang tak dikenal ditolak.
- `GET /health` tetap 200 saat outage; `GET /ready` menunjukkan status outage. Request data saat outage mengembalikan 503.
- Credential diperiksa sebelum delay. Delay default acak seragam 500–3000 ms; endpoint admin dapat mengatur range saat berjalan.
- Fixture JSON memuat 20 record; `MOCK_SEED_COUNT` (min. 20, default 20, max. 1000) dapat menambah seed simulasi. Lokasi semua fixture memakai volcano ID sintetis Tamriel yang dikenali tabel referensi mapper. Satu laporan baru ditambahkan setiap 10 detik, termasuk saat outage. Katalog mempertahankan fixture dan paling banyak 1000 laporan terbaru yang dihasilkan, sehingga snapshot penuh tetap terbatas.
- Skema v1 tidak mengirim `confidence_level`. Mengaktifkan v2 menampilkan field itu pada seluruh record tersimpan tanpa mengganti ID atau restart; kembali ke v1 menghilangkannya.

## Jalankan lokal

```bash
PVMBG_TOKEN='local-demo-token' go run ./services/pvmbg-mock/cmd/server
```

Port default `8082`. Endpoint data menunggu 0,5–3 detik sesuai kontrak. Contoh demo (gunakan token lokal yang sama):

```bash
curl -H 'Authorization: Bearer local-demo-token' 'http://localhost:8082/volcanic-reports'
curl -X POST -H 'Authorization: Bearer local-demo-token' -H 'Content-Type: application/json' -d '{"version":2}' 'http://localhost:8082/admin/schema-version'
curl -X POST -H 'Authorization: Bearer local-demo-token' -H 'Content-Type: application/json' -d '{"min_ms":3000,"max_ms":3000}' 'http://localhost:8082/admin/delay'
curl -X POST -H 'Authorization: Bearer local-demo-token' -H 'Content-Type: application/json' -d '{"enabled":true}' 'http://localhost:8082/admin/outage'
```

`PVMBG_TOKEN` wajib; `MOCK_SEED_COUNT`, `PVMBG_SCHEMA_VERSION`, `PVMBG_OUTAGE_ENABLED`, `PVMBG_DELAY_MIN_MS`, `PVMBG_DELAY_MAX_MS`, dan `MOCK_EVENT_INTERVAL` dapat diatur melalui environment. Gunakan token contoh hanya untuk simulasi lokal.

## Build/test

```bash
go test ./services/pvmbg-mock/...
go build ./services/pvmbg-mock/cmd/server
docker build -f services/pvmbg-mock/Dockerfile -t aat/pvmbg-mock .
```

Kontrak dan acceptance: [contract](../../docs/contract.md), [testing plan](../../docs/testing-plan.md).
