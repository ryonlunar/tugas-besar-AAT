# Script pengujian

## Setup kunci dan trial integrasi

Isi `.env` dengan secret lokal yang berbeda untuk setiap domain/client. Buat pasangan RSA sekali, lalu muat ke volume terpisah. Key tidak masuk build context atau Git:

```bash
mkdir -p .secrets
chmod 700 .secrets
(umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out .secrets/jwt-private.pem)
openssl pkey -in .secrets/jwt-private.pem -pubout -out .secrets/jwt-public.pem
python3 scripts/setup-jwt-keys.py
docker compose up --build -d --wait
python3 scripts/verify-integration.py --scenario all --output docs/evidence/integration-trial.json
```

Trial memakai Python 3, OpenSSL, dan Docker Compose. Jalankan dari root repo pada stack demo lokal: script mengubah kontrol PVMBG, menghentikan Auth/dashboard sementara, membangun ulang dashboard, dan menambah durable ketiga. Kontrol sumber dikembalikan; service yang dihentikan dinyalakan kembali. Pilihan `smoke`, `tokens`, dan `events` dapat dijalankan terpisah. Evidence berisi hasil/commit, tanpa token atau secret.

## Load test P2

`load/hazards.js` memakai profil k6 P2: 30 VU BMKG dan 30 VU PVMBG selama 90 detik, HTTP keep-alive, field-team dengan full view/limit 50, serta refresh token sekitar detik ke-50. Worker hazard tidak memakai think time.

Setelah Compose lengkap telah warm dan delay PVMBG disetel 3000 ms, buat folder evidence redacted lalu jalankan dari PowerShell:

```powershell
$env:FIELD_TEAM_CLIENT_SECRET = '<secret-lokal>'
$tcpJob = Start-Job { .\scripts\load\capture-tcp-connections.ps1 -Seconds 95 -OutputPath docs\evidence\p2-01\tcp.json }
k6 run --summary-export docs\evidence\p2-01\k6-summary.json .\scripts\load\hazards.js
$loadExit = $LASTEXITCODE
$tcpJob | Wait-Job | Receive-Job -ErrorAction Stop
if ($loadExit -ne 0) { throw "k6 thresholds failed" }
```

Set `AUTH_URL` atau `API_URL` bila port loopback berbeda. Ringkasan memisahkan attempts, success, controlled 429, dan unexpected error; setiap trend menampilkan p50/p95/p99, sedangkan threshold BMKG p95 adalah <300 ms. Script TCP mengukur koneksi established ke remote port API pada steady state, jadi bukti bukan hanya jumlah VU. Credential tidak ditulis ke script atau evidence.

Untuk Linux, jalankan collector pada host yang sama dengan k6. Isi secret melalui environment, bukan argumen CLI:

```bash
mkdir -p docs/evidence/p2-01
read -rs -p 'Field Team secret: ' FIELD_TEAM_CLIENT_SECRET; echo
export FIELD_TEAM_CLIENT_SECRET
python3 scripts/load/capture-tcp-connections.py --api-url "${API_URL:-http://127.0.0.1:8080}" --output docs/evidence/p2-01/tcp.json &
tcp_pid=$!
k6 run --summary-export docs/evidence/p2-01/k6-summary.json scripts/load/hazards.js
load_exit=$?
wait "$tcp_pid"; tcp_exit=$?
test "$load_exit" -eq 0 && test "$tcp_exit" -eq 0
```

`hazard_iterations` mencatat workload yang diminta; `hazard_attempts` hanya request yang dikirim setelah token tersedia. Kegagalan login/refresh menggagalkan threshold. Kedua sumber wajib memiliki request sukses; semua-429 bukan hasil lulus. Trend `*_duration` mencatat seluruh respons, sedangkan `*_success_duration` hanya 2xx. Threshold BMKG memakai `bmkg_success_duration`; `auth_duration` terpisah. Laporkan throughput, error noncontrolled, dan jumlah 429. Periksa sampel TCP selama steady state: minimal 50 koneksi selama minimal 60 detik, bukan hanya 60 VU.

Regression script: `node scripts/load/hazards.test.cjs`.

Script smoke yang tersedia:

- `bash scripts/smoke-source-mocks.sh` membangun dan menjalankan dua mock pada port lokal sementara; memeriksa seed BMKG, penolakan tanpa credential, toggle schema PVMBG v1/v2 dengan ID stabil, dan outage 503. Script memakai fixture/credential sintetis dan membersihkan prosesnya saat keluar.

Script ini tidak menguji Aggregator, Canonical Store, JetStream, Auth/API, consumer, atau acceptance P1–P5 secara penuh. Hasil yang benar-benar dijalankan dan batasnya dicatat di [verification.md](../docs/verification.md); jangan menyebut smoke mock sebagai E2E seluruh aplikasi.
