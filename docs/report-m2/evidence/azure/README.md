# Bukti pengujian Azure

Pengujian 22 November 2026 memakai tiga node k3s di Azure, region `indonesiacentral`, dengan versi aplikasi commit `1ab4ae6` (sama dengan uji Hetzner). Seluruh resource dibuat pukul 12.24 WIB dan dihapus pukul 13.23 WIB ([azure-timeline.txt](azure-timeline.txt)).

## Topologi

| Node | Peran | Ukuran VM | vCPU | RAM |
|---|---|---|---|---|
| `aat-dev` | Control plane dan worker | Standard_B2as_v2 | 2 | 8 GiB |
| `aat-worker-1` | Worker | Standard_B2als_v2 | 2 | 4 GiB |
| `aat-worker-2` | Worker | Standard_B2als_v2 | 2 | 4 GiB |

Setiap node adalah VM Ubuntu 24.04 dengan kernel dan disk OS 30 GB sendiri. Data `role=store` disimpan pada disk lokal node melalui `local-path`. Lalu lintas antar-node melewati satu VNet privat, sedangkan SSH hanya dibuka untuk satu alamat pengelola.

## Perbedaan dari klaster Hetzner

Langganan Azure for Students membatasi 6 vCPU dan 3 IP publik per region, sehingga klaster hanya memiliki tiga node. Penyesuaian yang dipakai:

- Request dan limit memori tetap sesuai Lampiran B.
- Keempat shard PostgreSQL tetap memiliki tiga salinan, kini satu salinan per node.
- `nodeSelector` Aggregator dan NATS dilonggarkan agar tiga replika tetap tersebar pada tiga node.
- `deploy-storage.sh` dan `check-cluster.py` memeriksa tiga node, bukan empat.
- Token NATS dibuat dengan awalan huruf. Perbaikan ini kemudian masuk ke `scripts/m2/prepare.py`.
- Sumber aktif: mock BMKG, mock PVMBG, dan Sensor Pantai profil `practice`. Feed Satelit tidak dijalankan.
- Generator beban P5.5 berjalan di `aat-dev`, sehingga latensi di bawah beban ikut dipengaruhi CPU node tersebut.

## Berkas

| Berkas | Isi |
|---|---|
| [check-cluster.log](check-cluster.log), [raw/exp/check-cluster-after.json](raw/exp/check-cluster-after.json) | Pemeriksaan klaster sebelum uji dan setelah node pulih. |
| [p14-p15-p16.log](p14-p15-p16.log), [raw/exp/p14.jsonl](raw/exp/p14.jsonl) | Worker dimatikan paksa, volume lamanya dihapus, node bergabung kembali dengan disk kosong, lalu redundansi diperiksa ulang. |
| [p3-schema-replay.log](p3-schema-replay.log) | Perubahan `rename_field`, pesan yang ditahan, adapter, dan replay oleh Ops. |
| [p3-field-reports-live.log](p3-field-reports-live.log) | Hak akses pesan gagal, 100 laporan, dan pengiriman ulang dengan kunci yang sama. Bagian P3.6c di berkas ini tidak dipakai karena laporan diproses pod lama yang sedang dihentikan. |
| [p36c-pod-kill.log](p36c-pod-kill.log) | Uji penghentian semua pod penerima setelah 20 laporan memperoleh 202. |
| [p55-priority-load.log](p55-priority-load.log) | Beban Media dan Tim Lapangan saat kapasitas API habis. |
| [p23-takeover.log](p23-takeover.log), [p24-clock-skew.log](p24-clock-skew.log), [p25-freeze.log](p25-freeze.log), [p26-p28-scale-rollout.log](p26-p28-scale-rollout.log) | Uji P2 tambahan. Laporan memakai hasil Hetzner untuk P2. |
| [scripts/](scripts/) | Skrip probe, snapshot Audit, dan skenario uji. Skrip dijalankan dari `aat-dev` dan membaca kredensial dari `.state/m2/credentials.json`. |
