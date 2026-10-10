# BMKG Mock

Menyediakan gempa dan peringatan tsunami sintetis untuk simulasi. Data ini bukan data BMKG produksi.

## Menjalankan

Memerlukan Go 1.23 atau lebih baru. Jalankan dari root repo:

```sh
BMKG_API_KEY='local-demo-key' go run ./services/bmkg-mock/cmd/server
```

Port default 8081, dengan delay tetap 100 ms. `BMKG_DELAY_MS` dapat diatur antara 50 dan 150 ms. Katalog awal berisi 20 gempa dan menambah satu gempa setiap 10 detik; jumlah seed dan interval diatur melalui `MOCK_SEED_COUNT` dan `MOCK_EVENT_INTERVAL`.

## Endpoint

| Endpoint | Fungsi |
|---|---|
| `GET /seismic-events` | Daftar gempa |
| `GET /tsunami-warnings` | Daftar peringatan tsunami |
| `GET /health` | Status proses |
| `GET /ready` | Kesiapan service |

Kedua endpoint data memerlukan header `X-BMKG-Key` dan menerima parameter `since` dalam format RFC3339; data dengan timestamp yang sama juga disertakan.

```sh
curl -H 'X-BMKG-Key: local-demo-key' http://localhost:8081/seismic-events
```

## Pengujian

```sh
go test ./services/bmkg-mock/...
```
