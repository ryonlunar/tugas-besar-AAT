# IF4031: Koordinasi Kebencanaan M1

Sistem yang menggabungkan data simulasi BMKG dan PVMBG, menyimpannya sebagai `HazardEvent`, lalu menyediakan akses melalui API dan distribusi event melalui NATS JetStream. Dibangun dengan Go, PostgreSQL, dan Docker Compose.

## Menjalankan aplikasi

Prasyarat: Docker dengan Compose, Python 3, dan OpenSSL. Jalankan command dari root repo.

1. Salin konfigurasi jika belum memiliki `.env`:

   ```sh
   cp .env.example .env
   ```

2. Isi seluruh secret yang masih kosong di `.env` dengan nilai berbeda.
3. Buat dan muat pasangan kunci RSA sesuai [setup kunci](scripts/README.md#setup-kunci).
4. Jalankan seluruh service:

   ```sh
   docker compose up --build --detach --wait --wait-timeout 180
   docker compose ps
   ```

Port default yang tersedia di `127.0.0.1`:

| Service | Port |
|---|---|
| Client API | 8080 |
| BMKG mock | 8081 |
| PVMBG mock | 8082 |
| Aggregator | 8083 |
| Auth | 8084 |

PostgreSQL, NATS, dan consumer diakses melalui jaringan container. Konfigurasi lengkap tersedia di [.env.example](.env.example).

Periksa kesiapan API:

```sh
curl -fsS http://127.0.0.1:8080/ready
```

Untuk menghentikan stack dengan tetap menyimpan volume data:

```sh
docker compose down
```

`.env` dan kunci RSA tidak disimpan di Git.

## Struktur repo

| Lokasi | Isi |
|---|---|
| [services/](services/) | Mock sumber, Aggregator, Auth, Client API, dan consumer; masing-masing memiliki Dockerfile |
| [contracts/](contracts/README.md) | JSON Schema dan contoh data |
| [docker-compose.yml](docker-compose.yml) | Konfigurasi container, jaringan, dan volume seluruh service |
| [scripts/](scripts/README.md) | Setup kunci, pengujian integrasi, dan load test |
| [docs/report/](docs/report/README.md) | Laporan M1, sumber LaTeX, dan bukti pengujian |

Compose di root menjalankan seluruh service. Aggregator membaca kedua mock dan menyimpan data ke PostgreSQL. Client API membaca melalui Aggregator, sedangkan dashboard dan notifier menerima event dari NATS dengan durable consumer masing-masing.

Untuk menjalankan consumer demo ketiga:

```sh
docker compose --profile demo up --build --detach --wait third-consumer
```
