<a id="readme-top"></a>

<div align="center">
  <h3 align="center">IF4031/AAT - Koordinasi Kebencanaan M1</h3>
  <p align="center">
    Ingest dan distribusi event kebencanaan berbasis kontrak M1<br/>
    Go · PostgreSQL · NATS JetStream · Docker Compose
  </p>
</div>

<div align="center">
  <br/>
  <img src="https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white" />
  <img src="https://img.shields.io/badge/PostgreSQL-4169E1?style=for-the-badge&logo=postgresql&logoColor=white" />
  <img src="https://img.shields.io/badge/NATS_JetStream-27AAE1?style=for-the-badge&logo=natsdotio&logoColor=white" />
  <img src="https://img.shields.io/badge/Docker_Compose-2496ED?style=for-the-badge&logo=docker&logoColor=white" />
</div>

---

<details>
  <summary>Daftar Isi</summary>
  <ol>
    <li><a href="#status-singkat">Status singkat</a></li>
    <li><a href="#baca-sebelum-coding">Baca sebelum coding</a></li>
    <li><a href="#arsitektur">Arsitektur</a></li>
    <li><a href="#struktur-dan-pembagian">Struktur dan pembagian</a></li>
    <li><a href="#port-default">Port default</a></li>
    <li><a href="#menjalankan-slice-docker-yang-tersedia">Menjalankan aplikasi</a></li>
    <li><a href="#compose-aplikasi-penuh">Compose aplikasi penuh</a></li>
    <li><a href="#konfigurasi">Konfigurasi</a></li>
  </ol>
</details>

## Status singkat

<a id="status-singkat"></a>

**Komponen M1 tersedia: dua mock, Aggregator, PostgreSQL, NATS, Auth, Client API, dan dua consumer. Trial integrasi dan load test tersedia di `scripts/`; hasil pengujian ada di `docs/evidence/`.**

Stack pilihan kelompok: Go, PostgreSQL, NATS JetStream, Docker Compose. Kontrak v1 ditetapkan berdasarkan PDF tugas dan sumber teknis primer; bukan lagi placeholder endpoint/skema.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Baca sebelum coding

<a id="baca-sebelum-coding"></a>

1. [Wire contract](contracts/README.md): JSON Schema dan contoh sintetis lintas-service.
2. [Setup dan pengujian](scripts/README.md): kunci RSA, trial P1–P5, dan load test.
3. [Evidence](docs/evidence/): hasil pengujian beserta commit dan profil yang digunakan.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Arsitektur

<a id="arsitektur"></a>

![Diagram arsitektur kontrak M1](docs/assets/arch.png)

Auth dan Client API terpisah. Consumer memiliki durable dan SQLite sendiri. Hanya Aggregator mengakses PostgreSQL Canonical Store. Consumer tidak memanggil fungsi Aggregator atau membuka databasenya.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Struktur dan pembagian

<a id="struktur-dan-pembagian"></a>

| Lokasi | Pemilik | Fokus |
|---|---|---|
| `services/bmkg-mock/`, `services/pvmbg-mock/`, `services/aggregator/` | Hazim | Source, mapping, polling, PostgreSQL, API internal, outbox, wiring Compose lokal |
| `services/client-api/`, `services/auth/` | Bimo | Otorisasi, token, API read, bukti load/resilience |
| `services/dashboard-consumer/`, `services/notifier-consumer/` | Refki | Consumer JetStream, Compose aplikasi penuh, network antarservice, deployment/integrasi E2E |
| `contracts/`, `docs/` | Bersama | Wire schema dan evidence |

Tiap service memiliki entrypoint `cmd/server/`, logic privat `internal/`, serta Dockerfile. Jangan mengimpor business logic service lain. `cmd/` dan `internal/` tingkat root hanya menyimpan catatan kompatibilitas struktur lama; jangan menambahkan logic di sana.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Port default

<a id="port-default"></a>

BMKG **8081**, PVMBG **8082**, Client API **8080** mengikuti spesifikasi. Aggregator 8083, Auth 8084, dashboard consumer 8085, notifier consumer 8086 adalah keputusan proyek. Jika port aplikasi diubah, sesuaikan listener, base URL, dan dokumentasi bersama.

PostgreSQL 5432 dan NATS 4222 hanya di jaringan internal; tidak di-publish ke host. NATS monitoring 8222 tidak di-publish ke host; akses melalui `docker compose exec`. Monitoring tidak membawa mekanisme autentikasi sendiri; jangan expose ke internet.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Menjalankan aplikasi

<a id="menjalankan-slice-docker-yang-tersedia"></a>

Prasyarat: Docker Engine/Desktop, Compose, Python 3, dan OpenSSL. Jalankan dari root repo.

1. Salin `.env.example` ke `.env` jika belum ada. Isi `COMPOSE_PROJECT_NAME` dan sembilan secret berbeda: `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_API_KEY`, `CONSUMER_DEMO_KEY`, `POSTGRES_PASSWORD`, `NATS_TOKEN`, `MEDIA_CLIENT_SECRET`, `FIELD_TEAM_CLIENT_SECRET`, `BNPB_INTERNAL_CLIENT_SECRET`.
2. Buat pasangan RSA dan muat ke volume sesuai [setup kunci](scripts/README.md#setup-kunci-dan-trial-integrasi).
3. Jalankan:

```bash
docker compose config --quiet
docker compose up --build --detach --wait --wait-timeout 180
docker compose ps
```

Sembilan service berjalan secara default. Mock, Aggregator, Auth, dan Client API hanya dipublish ke loopback host. PostgreSQL, NATS, dan consumer hanya dapat diakses melalui jaringan container atau `docker compose exec`. Tiap service menerima konfigurasi miliknya. Aggregator hanya menunggu PostgreSQL; polling sumber dan relay NATS pulih secara independen.

```bash
curl -fsS http://127.0.0.1:8080/ready
docker compose exec -T nats wget -qO- 'http://127.0.0.1:8222/healthz?js-enabled-only=true'
```

`.env`, kunci RSA, dan output lengkap `docker compose config` berisi secret dan tidak masuk Git.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Compose aplikasi penuh

<a id="compose-aplikasi-penuh"></a>

Compose root membangun Dockerfile masing-masing service. Untuk menjalankan consumer demo ketiga:

```bash
docker compose --profile demo up --build --detach --wait third-consumer
```

Untuk membangun ulang satu service, gunakan `docker compose up --build --detach --no-deps --force-recreate <service>`. Trial otomatis memeriksa API dan container lain selama rebuild.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Konfigurasi

<a id="konfigurasi"></a>

`.env.example` menetapkan port/base URL, secret per domain, source seed/delay/polling, timeout, pool DB, admission, JWT/refresh, stream/durable/retention, dan outbox retry. Default penting:

- Poll 5 s; timeout BMKG 1 s, PVMBG 4 s; stale threshold 15 s.
- Token TTL 60 s; refresh expiry absolut 24h; private key Auth, public key Client API.
- Stream `HAZARD_EVENTS`, fixed producer subjects `hazard.events.v1.BMKG`/`hazard.events.v1.PVMBG` (bukan override env), `NATS_PUBLISH_TIMEOUT=2s`, durable dashboard-v1/notifier-v1.
- Snapshot polling M1; mutation event + outbox atomik; ACK consumer sesudah side effect commit.

Konfigurasi lengkap ada di [.env.example](.env.example). Compose menetapkan alamat PostgreSQL/NATS serta path volume kunci/state; nilai contoh tersebut juga dipakai untuk menjalankan binary langsung. Header korelasi selalu `X-Correlation-ID`; batas pagination adalah 50 default dan 100 maksimum.

[Trial integrasi](scripts/README.md) menyediakan skenario `smoke` (mapping, hak akses, outage, schema), `tokens` (expiry/refresh), dan `events` (consumer mati, catch-up, rebuild, consumer ketiga). [Load test P2](scripts/README.md#load-test-p2) mengatur delay PVMBG 3000 ms dan mengukur latency serta koneksi TCP.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>


