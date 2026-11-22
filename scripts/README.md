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

## Load test M2 P2

Untuk profil 10.000 VU, jalankan tiga replika Client API dengan limit memori 256 MiB per pod. Dari control plane, siapkan namespace dan tunggu ketiga replika siap.

```sh
ns=sistem-informasi-kebencanaan-bnpb
kubectl -n "$ns" scale deployment/client-api --replicas=3
kubectl -n "$ns" rollout status deployment/client-api
```

Di host k6, siapkan Linux, Python 3, dan k6. Salin `load/m2-p2.js` dan `load/run-m2-p2.py` ke direktori yang sama. Sesuaikan alamat API dan Auth dengan klaster.

```sh
export P2_API_URL=http://10.77.0.11:30080
export P2_AUTH_URL=http://10.77.0.11:30084
read -rsp 'Field Team secret ' FIELD_TEAM_CLIENT_SECRET
printf '\n'
printf '%s\n' "$FIELD_TEAM_CLIENT_SECRET" | python3 run-m2-p2.py p2-scale
unset FIELD_TEAM_CLIENT_SECRET
```

Profil bawaan mencapai 10.000 VU dalam 45 detik dan mempertahankannya selama 150 detik. Tiap VU bergantian membaca halaman BMKG dan PVMBG dengan jeda delapan detik. Runner mengambil dan menyegarkan token melalui Auth. VU membaca token dari relay di localhost untuk memisahkan pengujian kapasitas baca dari lonjakan login.

Dari control plane, ubah replika Aggregator menjadi lima lalu dua ketika `active-vus.jsonl` menunjukkan 10.000 VU aktif. Tunggu setiap perubahan selesai dengan `kubectl rollout status deployment/aggregator` di namespace aplikasi. Simpan output perintah, log upstream, dan hasil pemeriksaan duplikat bersama `result.json` dan `active-vus.jsonl`.

Untuk membandingkan throughput, set `P2_RATE` dan `P2_SECONDS` ke nilai yang sama pada pengujian satu replika dan tiga replika. Untuk menguji rollout, jalankan profil beban yang sama selama perubahan image dan simpan waktu perubahan beserta jumlah permintaan yang gagal.
