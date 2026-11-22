# Bukti pengujian Hetzner

Pengujian 22 November 2026 memakai empat node k3s dan satu VM k6 di Helsinki. Versi aplikasi adalah commit 1ab4ae6. Untuk mengulang uji, lihat [provisioning Hetzner](../../../../infra/hetzner/README.md) dan [langkah uji P2](../../../../scripts/README.md#load-test-m2-p2). [Workload k6](../../../../scripts/load/m2-p2.js) mendefinisikan permintaan dan skenario. [Runner](../../../../scripts/load/run-m2-p2.py) mengelola token dan menyimpan ringkasan k6. Kedua berkas dapat dicocokkan dengan SHA-256 pada informasi setup di beban-k6.json.

| Berkas | Isi |
|---|---|
| [cluster-check.json](cluster-check.json), [audit-live.json](audit-live.json) | Pemeriksaan replika, isolasi jaringan penyimpanan, trace, dan lokasi salinan per kejadian. |
| [beban-k6.json](beban-k6.json) | Konfigurasi, digest image, hasil scaling dan rollout, empat pasangan throughput, snapshot penempatan shard, dan ringkasan uji gangguan. |
| [active-vus.jsonl](active-vus.jsonl), [k6-vu.dat](k6-vu.dat) | Sampel k6 asli dan koordinat grafik yang dihitung relatif terhadap sampel pertama. |
| [owner-replacement-upstream.json](owner-replacement-upstream.json) | Log polling dari mock saat pergantian pemilik. |
| [foundation-fixtures.log](foundation-fixtures.log) | Keluaran pengujian pada basis data PostgreSQL fixture. |
