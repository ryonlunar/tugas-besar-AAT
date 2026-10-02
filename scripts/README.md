# Script pengujian

## Load test P2

`load/hazards.js` memakai profil k6 P2: 30 VU BMKG dan 30 VU PVMBG selama 90 detik, HTTP keep-alive, field-team dengan full view/limit 50, serta refresh token sekitar detik ke-50. Worker hazard tidak memakai think time.

Setelah Compose lengkap telah warm dan delay PVMBG disetel 3000 ms, buat folder evidence redacted lalu jalankan dari PowerShell:

```powershell
$env:FIELD_TEAM_CLIENT_SECRET = '<secret-lokal>'
Start-Job { .\scripts\load\capture-tcp-connections.ps1 -Seconds 95 -OutputPath docs\evidence\p2-01\tcp.json }
k6 run -e FIELD_TEAM_CLIENT_SECRET=$env:FIELD_TEAM_CLIENT_SECRET --summary-export docs\evidence\p2-01\k6-summary.json .\scripts\load\hazards.js
```

Set `AUTH_URL` atau `API_URL` bila port loopback berbeda. Ringkasan memisahkan attempts, success, controlled 429, dan unexpected error; setiap trend menampilkan p50/p95/p99, sedangkan threshold BMKG p95 adalah <300 ms. Script TCP mengukur koneksi established ke remote port API pada steady state, jadi bukti bukan hanya jumlah VU. Credential tidak ditulis ke script atau evidence.

Script smoke yang tersedia:

- `bash scripts/smoke-source-mocks.sh` membangun dan menjalankan dua mock pada port lokal sementara; memeriksa seed BMKG, penolakan tanpa credential, toggle schema PVMBG v1/v2 dengan ID stabil, dan outage 503. Script memakai fixture/credential sintetis dan membersihkan prosesnya saat keluar.

Script ini tidak menguji Aggregator, Canonical Store, JetStream, Auth/API, consumer, atau acceptance P1–P5 secara penuh. Hasil yang benar-benar dijalankan dan batasnya dicatat di [verification.md](../docs/verification.md); jangan menyebut smoke mock sebagai E2E seluruh aplikasi.
