# Bukti pengujian M2

Lampiran A pada laporan memetakan setiap kriteria ke bukti dan batas pengujiannya.

| Problem | Hasil dan cara menelusuri |
|---|---|
| P1 | [Klaster Hetzner](hetzner/README.md) mencatat penempatan shard dan Audit API. [Log impor 500 baris](bmkg/import-500.log) berasal dari [varian fixture PostgreSQL](bmkg/import-500.patch). Hasil penangguhan worker dan perpindahan primary ada di [pemulihan-shard.json](pemulihan-shard.json). Kunci p1.analytics_dashboard_audit pada [uji lokal](latihan-k3s.json) memuat rentang waktu dan hasil dasbor yang dicocokkan dengan Audit. Kehilangan worker dengan penggantian disk tercatat di [p14-p15-p16.log](azure/p14-p15-p16.log) pada [klaster Azure](azure/README.md). |
| P2 | [Hasil Hetzner](hetzner/README.md) mencakup workload, pengamatan VU, log mock, dan hasil dari tiap konfigurasi. |
| P3 | Kunci p3 pada [uji lokal](latihan-k3s.json) memuat hasil replay, status akses Ops, correlation ID, dan tanda terima laporan. [Log fixture](hetzner/foundation-fixtures.log) mencatat uji konkurensi laporan dan penanganan input PVMBG. Uji 20 laporan saat seluruh pod Aggregator dihapus dan replay 209 pesan ada di [klaster Azure](azure/README.md). |
| P4 | Kunci p4 pada [uji lokal](latihan-k3s.json) memuat posisi feed, pergantian pemilik, dan metrik setelah koneksi pulih. [Log fixture](hetzner/foundation-fixtures.log) mencatat uji pemulihan pemrosesan Satelit. |
| P5 | Kunci p5 pada [uji lokal](latihan-k3s.json) memuat status sumber, latensi, dan tanda terima latihan. [Masukan terkendali](masukan-terkendali.json) mencatat validasi pengamatan dan pengiriman ulang ke penerima. Prioritas saat beban berlebih tercatat di [p55-priority-load.log](azure/p55-priority-load.log) pada [klaster Azure](azure/README.md). |

Untuk mengulang fixture impor 500 baris, terapkan [patch uji](bmkg/import-500.patch) pada checkout pengujian yang terpisah, lalu jalankan [check-foundation.py](../../../scripts/m2/check-foundation.py) pada control plane. Fixture menghasilkan CSV dari data sintetis. Pengecekan dasbor dan tanda terima dirangkum di [panduan k3s](../../../infra/k8s/README.md#sensor-pantai-and-analytics).
