# bmkg-mock

Pemilik: Hazim. Mock BMKG ini sudah dapat dijalankan sebagai service HTTP mandiri; ia menyediakan data simulasi, bukan data BMKG produksi.

## Implementasi

- `GET /seismic-events?since=<RFC3339>` dan `GET /tsunami-warnings?since=<RFC3339>` memakai header `X-BMKG-Key`.
- `GET /health` tanpa autentikasi.
- Credential diperiksa sebelum delay tetap (default 100 ms). Timestamp `since` inklusif; parameter tak dikenal atau timestamp invalid ditolak.
- Fixture JSON memuat 20 gempa sintetis; sebagian memiliki warning terkait. `MOCK_SEED_COUNT` (min. 20, default 20, max. 1000) dapat menambah seed simulasi; waktu relatif terhadap startup. ID memuat run ID acak. Satu event baru ditambahkan setiap `MOCK_EVENT_INTERVAL` (default 10 detik). Katalog mempertahankan fixture dan paling banyak 1000 event terbaru yang dihasilkan, sehingga snapshot penuh tetap terbatas.
- Katalog aman untuk request bersamaan. Warning memakai waktu publikasi internal untuk filter `since`; waktu tersebut tidak muncul pada respons.

## Jalankan lokal

```bash
BMKG_API_KEY='local-demo-key' go run ./services/bmkg-mock/cmd/server
```

Port default `8081`. Untuk mencoba:

```bash
curl -H 'X-BMKG-Key: local-demo-key' 'http://localhost:8081/seismic-events'
curl -H 'X-BMKG-Key: local-demo-key' 'http://localhost:8081/tsunami-warnings'
curl 'http://localhost:8081/health'
```

`BMKG_API_KEY` wajib; `BMKG_DELAY_MS` hanya boleh 50–150 ms. Gunakan key contoh hanya untuk simulasi lokal.

## Build/test

```bash
go test ./services/bmkg-mock/...
go build ./services/bmkg-mock/cmd/server
# Build image dengan root repo sebagai build context:
docker build -f services/bmkg-mock/Dockerfile -t aat/bmkg-mock .
```

Kontrak, fixture, dan acceptance: [contract](../../docs/contract.md), [testing plan](../../docs/testing-plan.md).
