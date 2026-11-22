<a id="readme-top"></a>

<div align="center">
  <h3 align="center">IF4031/AAT - Koordinasi Kebencanaan M2</h3>
  <p align="center">
    Tugas Besar IF4031 Arsitektur Aplikasi Terdistribusi<br/>
    Milestone 2 · DikejarDeadline
  </p>
</div>

<div align="center">
  <br/>
  <img src="https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/PostgreSQL-4169E1?style=for-the-badge&logo=postgresql&logoColor=white" alt="PostgreSQL" />
  <img src="https://img.shields.io/badge/NATS_JetStream-27AAE1?style=for-the-badge&logo=natsdotio&logoColor=white" alt="NATS JetStream" />
  <img src="https://img.shields.io/badge/K3s-FFC61C?style=for-the-badge&logo=k3s&logoColor=black" alt="K3s" />
</div>

---

## Latar belakang

Tugas besar ini membangun sistem koordinasi kebencanaan BNPB untuk Media, Tim Lapangan, dan BNPB Internal. M2 menambahkan Feed Satelit dan Sensor Pantai, membagi penyimpanan ke empat shard PostgreSQL, serta menjalankan replika Aggregator dan JetStream pada beberapa node. Client API menyediakan akses berdasarkan hak client, termasuk Dasbor Analitik dan pengiriman laporan Tim Lapangan.

[Laporan M2](docs/report-m2/IF4031_M2_DikejarDeadline.pdf) · [Panduan verifikasi M2](docs/m2-verification.md) · [Panduan M1](README-m1.md)

## Menjalankan sistem

Siapkan empat VM Ubuntu 24.04 x86_64 dengan kernel terpisah dan jaringan privat. Pada host Linux atau WSL, siapkan Git, Python 3, GNU Make, dan Multipass. Panduan [k3s](infra/k8s/README.md#multi-node-profile) menjelaskan cara memasang k3s dengan penyedia VM lain. Untuk pengujian beban, jalankan k6 di host terpisah dari pod aplikasi.

```sh
git clone https://github.com/ryonlunar/tugas-besar-AAT.git
cd tugas-besar-AAT
git checkout milestone-2
make up
make deploy
"${AAT_MULTIPASS_BIN:-multipass}" exec aat-dev -- bash -lc 'cd ~/aat && make cluster-check'
```

`up` menyiapkan node dan k3s. Setelah node siap, `deploy` menjalankan deployment aplikasi dalam satu perintah. Di WSL, set `AAT_MULTIPASS_BIN` ke path `multipass.exe` bila executable Windows belum ada di PATH. Hasil pemeriksaan tersimpan di `~/aat/.state/m2/check-cluster.json` pada control plane.

### Topologi

| Profil | Host | Node k3s | Host pengujian beban |
| --- | --- | --- | --- |
| Multipass | Laptop Windows Refki, 12 logical CPU dan 32 GiB RAM | `aat-dev` sebagai control plane sekaligus worker (4 vCPU, 8 GiB), tiga worker (masing-masing 2 vCPU, 3 GiB). Disk lokal 30 GiB per VM. | Mesin terpisah jika menjalankan load test M2. |
| Hetzner Helsinki | Host fisik dikelola penyedia. Tabel ini mencatat alokasi VM. | Control plane CCX33 (8 vCPU, 32 GB), satu worker CCX23 (4 vCPU, 16 GB), dan dua worker CPX32 (masing-masing 4 vCPU, 8 GB). | CPX52 (12 vCPU, 24 GB). |

Empat node M2 menampung tiga replika Client API, tiga Aggregator, empat shard dengan tiga salinan per shard, dan tiga node JetStream. Jalur antar-node ditampilkan pada diagram deployment di laporan. Gunakan [OpenTofu](infra/hetzner/README.md) untuk mereproduksi profil Hetzner.

### Konfigurasi

[.env.example](.env.example) mencantumkan variabel dan default. Pada k3s, ubah nilai nonsecret di `~/aat/.state/m2/runtime.env`, lalu jalankan kembali `bash scripts/m2/deploy-cluster.sh` dari VM control plane. Jika hanya konfigurasi env yang berubah, jalankan `kubectl -n sistem-informasi-kebencanaan-bnpb rollout restart deployment/<service>` agar pod terkait membaca nilai baru. Deployment menyimpan konfigurasi sebagai ConfigMap, kredensial dan kunci sebagai Secret.

| Kebutuhan | Masukan |
| --- | --- |
| Katalog BMKG | `~/aat/.state/m2/bmkg-catalog.csv` sebelum deploy. |
| Feed Satelit | `SATELLITE_BASE_URL` dan Secret `satellite-secret` dengan key `token`. |
| Sensor Pantai | `PANTAI_BASE_URL`, Secret `pantai-token`, serta profil dan key sesuai sesi. |
| Audit dan client | `AUDIT_TOKEN` dan secret masing-masing client di `.state/m2/credentials.json`. |
| P3 dan P2 | `PVMBG_SCHEMA_ADAPTERS`, `FIELD_REPORT_PROCESSING_ENABLED`, batas request API dan Aggregator, serta [konfigurasi k6](scripts/README.md#load-test-m2-p2). |

## Pengujian per problem

Jalankan pengujian dari `~/aat` pada control plane. Gunakan `make help` untuk melihat target. [Makefile](Makefile) memuat perintah yang dijalankan dan [panduan verifikasi](docs/m2-verification.md) memuat langkah lengkap beserta hasil yang diperiksa.

| Problem | Pemicu latihan | Bukti utama |
| --- | --- | --- |
| [P1 · Penyimpanan dan analitik](docs/m2-verification.md#p1-penyimpanan-dan-analitik) | `make pipeline-check` dan `make tester-run SCENARIO=kill-store`. Uji hapus disk memakai klaster disposable. | Sebaran shard, checksum Audit, hasil baca lengkap atau bertanda parsial, redundansi kembali, dan Dasbor Analitik. |
| [P2 · Koordinasi Aggregator](docs/m2-verification.md#p2-koordinasi-aggregator) | Tester Agent `kill-leader` dan `clock-skew`, lalu [runner k6](scripts/README.md#load-test-m2-p2) dengan perubahan replika dan image. | Waktu pengambilalihan, log polling, jumlah VU aktif, rasio throughput, dan hasil rollout. |
| [P3 · Evolusi skema dan laporan](docs/m2-verification.md#p3-evolusi-skema-dan-laporan) | Ubah mode mock PVMBG melalui `POST /admin/schema-version`. Jalankan `make pipeline-recovery` dan uji `POST /field-reports`. | Payload mentah, replay berurutan, pembatasan akses Ops, serta tanda terima 202 yang bertahan saat pod diganti. |
| [P4 · Feed Satelit](docs/m2-verification.md#p4-feed-satelit) | Aktifkan Feed Satelit, gunakan probe sesi, lalu hapus pod pemilik sumber pada latihan. | Posisi kursor dan p95 penyimpanan, waktu mengejar ketertinggalan, serta selisih terhadap referensi. |
| [P5 · Sensor Pantai](docs/m2-verification.md#p5-sensor-pantai) | Jalankan `make coastal-check`, hentikan Sensor Pantai sementara, lalu jalankan `make field-alerts-check` dengan daftar pembanding. | Respons untuk sumber lain, pemulihan sensor, jumlah tanda terima kritis, dan p95 dari sensor hingga penerima. |

[Pasang dan jalankan Tester Agent](docs/m2-verification.md#tester-agent) untuk latihan dan ujian. [Observabilitas](docs/m2-verification.md#observabilitas) memuat cara menemukan metrik, correlation ID, trace, dan keluaran Audit untuk demonstrasi.

Untuk menghentikan VM Multipass sambil menyimpan data latihan, jalankan `make stop` dari host. Pada Hetzner, simpan keluaran pengujian lalu jalankan `infra/hetzner/cloud.sh down <nama-sesi>` dari host.
