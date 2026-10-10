# Script pengujian

Langkah setup, konfigurasi, dan pengujian P1–P5 tersedia di [README utama](../README.md).

| Script | Fungsi |
|---|---|
| `setup-jwt-keys.py` | Memeriksa pasangan RSA dan memuatnya ke volume Auth serta Client API |
| `verify-integration.py` | Trial `smoke`, `tokens`, `events`, atau `all`; hasil disimpan melalui `--output` |
| `load/hazards-monotonic.py` | Load test HTTP dengan timer monotonic |
| `load/capture-tcp-connections.py` | Mencatat koneksi TCP di Linux; memerlukan `ss` |
| `load/hazards.js` | Profil load test alternatif menggunakan k6 |
| `load/capture-tcp-connections.ps1` | Collector TCP untuk Windows |
| `smoke-source-mocks.sh` | Menjalankan kedua mock pada port sementara dan memeriksa credential, seed, skema, serta outage |

## Pengujian mock saja

Jalankan dari root repo dengan Go 1.23 atau lebih baru, Python 3, dan curl:

```bash
bash scripts/smoke-source-mocks.sh
```

Script membersihkan proses mock saat selesai.

## Alternatif tooling load test

Untuk menggunakan k6, ganti command `hazards-monotonic.py` pada langkah P2 di README utama dengan command berikut. Environment dan collector TCP tetap digunakan:

```bash
k6 run --summary-export "$EVIDENCE_DIR/k6-summary.json" scripts/load/hazards.js
```

Hasil latency negatif tidak valid. Di Windows, jalankan collector ini pada terminal PowerShell terpisah selama load test; sesuaikan port dan lokasi output:

```powershell
.\scripts\load\capture-tcp-connections.ps1 -ApiPort 8080 -Seconds 110 -IntervalSeconds 1 -OutputPath tcp.json
```
