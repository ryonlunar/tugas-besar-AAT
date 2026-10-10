# Setup dan pengujian

Command berikut dijalankan dari root repo menggunakan Bash. Siapkan `.env` sesuai [README utama](../README.md#menjalankan-aplikasi).

## Setup kunci

Memerlukan Docker, Python 3, dan OpenSSL. Buat pasangan kunci RSA sekali, lalu muat ke volume Auth dan Client API:

```bash
mkdir -p .secrets
chmod 700 .secrets
if [ ! -e .secrets/jwt-private.pem ] && [ ! -e .secrets/jwt-public.pem ]; then
  (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out .secrets/jwt-private.pem)
  openssl pkey -in .secrets/jwt-private.pem -pubout -out .secrets/jwt-public.pem
fi
python3 scripts/setup-jwt-keys.py
```

Jika pasangan kunci sudah ada, cukup jalankan `setup-jwt-keys.py`. Volume kunci tetap tersimpan setelah `docker compose down`.

## Pengujian integrasi

Jalankan pada stack lokal yang sudah aktif:

```bash
python3 scripts/verify-integration.py --scenario all --output docs/evidence/integration-trial.json
```

Script membaca `.env` dan menyimpan hasil dalam JSON. Skenario dapat dijalankan terpisah dengan mengganti `all`:

| Skenario | Pemeriksaan |
|---|---|
| `smoke` | Pemetaan data, hak akses, outage sumber, dan perubahan skema |
| `tokens` | Kedaluwarsa dan refresh token |
| `events` | Consumer berhenti dan catch-up, rebuild container, serta consumer ketiga |

Pengujian mengubah kontrol PVMBG dan menghentikan beberapa service sementara. Setelah selesai, script mengembalikan kontrol sumber dan menyalakan kembali service tersebut.

## Load test P2

Profil pengujian menggunakan 30 worker per sumber selama 90 detik, HTTP keep-alive, akses Field Team dengan `view=full`, dan refresh token sebelum kedaluwarsa. Gunakan TTL access token default 60 detik.

Setelah kedua sumber berstatus fresh, muat environment dan atur delay PVMBG menjadi 3000 ms:

```bash
set -a
. ./.env
set +a
export API_URL="http://127.0.0.1:${API_HTTP_PORT:-8080}"
export AUTH_URL="http://127.0.0.1:${AUTH_HTTP_PORT:-8084}"
curl -fsS -X POST "http://127.0.0.1:${PVMBG_MOCK_HTTP_PORT:-8082}/admin/delay" \
  -H "Authorization: Bearer $PVMBG_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"min_ms":3000,"max_ms":3000}'
sleep 5
```

Di Linux, jalankan load test dan pencatatan koneksi TCP bersamaan pada host yang sama. Collector memerlukan `ss`:

```bash
mkdir -p docs/evidence/p2-01
python3 scripts/load/capture-tcp-connections.py --api-url "$API_URL" \
  --seconds 110 --output docs/evidence/p2-01/tcp.json &
tcp_pid=$!
python3 scripts/load/hazards-monotonic.py --output docs/evidence/p2-01/monotonic-summary.json
load_exit=$?
wait "$tcp_pid"
tcp_exit=$?
test "$load_exit" -eq 0 && test "$tcp_exit" -eq 0
```

Load test memakai timer monotonic dan mengukur latency sampai seluruh body respons diterima. Ringkasan memuat p50/p95/p99, throughput, respons 429, dan error. Kriteria lulus mencakup BMKG p95 <300 ms dan error di luar 429 <1%; bukti TCP diperiksa terpisah untuk memastikan sedikitnya 50 koneksi selama 60 detik.

Jika menggunakan k6, ganti command load test dengan:

```bash
k6 run --summary-export docs/evidence/p2-01/k6-summary.json scripts/load/hazards.js
```

Hasil latency negatif tidak valid. Setelah pengujian, kembalikan delay PVMBG melalui endpoint yang sama dengan body `{"min_ms":500,"max_ms":3000}`.

Untuk Windows, gunakan collector berikut di terminal PowerShell terpisah selama load test; sesuaikan `-ApiPort` jika port API berubah:

```powershell
.\scripts\load\capture-tcp-connections.ps1 -ApiPort 8080 -Seconds 110 -IntervalSeconds 1 -OutputPath docs\evidence\p2-01\tcp.json
```

## Pengujian mock

Memerlukan Go 1.23 atau lebih baru:

```bash
bash scripts/smoke-source-mocks.sh
```

Script menjalankan kedua mock pada port sementara untuk memeriksa autentikasi, seed data, perubahan skema PVMBG, dan outage. Proses mock dibersihkan saat script selesai.
