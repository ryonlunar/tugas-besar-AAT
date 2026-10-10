<a id="readme-top"></a>

<div align="center">
  <h3 align="center">IF4031/AAT - Koordinasi Kebencanaan M1</h3>
  <p align="center">
    Tugas Besar IF4031 Arsitektur Aplikasi Terdistribusi<br/>
    Milestone 1 · DikejarDeadline
  </p>
</div>

<div align="center">
  <br/>
  <img src="https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/PostgreSQL-4169E1?style=for-the-badge&logo=postgresql&logoColor=white" alt="PostgreSQL" />
  <img src="https://img.shields.io/badge/NATS_JetStream-27AAE1?style=for-the-badge&logo=natsdotio&logoColor=white" alt="NATS JetStream" />
  <img src="https://img.shields.io/badge/Docker_Compose-2496ED?style=for-the-badge&logo=docker&logoColor=white" alt="Docker Compose" />
</div>

---

<details>
  <summary>Daftar Isi</summary>
  <ol>
    <li><a href="#latar-belakang">Latar belakang</a></li>
    <li><a href="#arsitektur">Arsitektur</a></li>
    <li><a href="#menjalankan-aplikasi">Menjalankan aplikasi</a></li>
    <li><a href="#variabel-konfigurasi">Variabel konfigurasi</a></li>
    <li><a href="#pengujian-per-problem">Pengujian per problem</a>
      <ul>
        <li><a href="#problem-1">P1: Arsitektur &amp; Interoperabilitas Data</a></li>
        <li><a href="#problem-2">P2: Concurrency, Availability, &amp; Graceful Degradation</a></li>
        <li><a href="#problem-3">P3: Service-to-Service Authentication &amp; Trust</a></li>
        <li><a href="#problem-4">P4: Arsitektur Microservice &amp; Flexible Storage</a></li>
        <li><a href="#problem-5">P5: Fan-out Event Distribution/Pub-Sub</a></li>
      </ul>
    </li>
    <li><a href="#log-dan-menghentikan-sistem">Log dan menghentikan sistem</a></li>
    <li><a href="#lokasi-kode-dan-laporan">Lokasi kode dan laporan</a></li>
  </ol>
</details>

## Latar belakang

Repositori ini berisi implementasi Milestone 1 untuk tugas besar IF4031 Arsitektur Aplikasi Terdistribusi. Tugas ini mengangkat kebutuhan BNPB untuk menyatukan informasi gempa dan tsunami dari BMKG serta aktivitas gunung api dari PVMBG, agar dapat digunakan oleh Media, Tim Lapangan, dan BNPB internal.

Pada implementasi ini, kedua instansi disimulasikan melalui mock dengan data sintetis. Sistem menyatukan data dalam format `HazardEvent` dan menyediakan akses melalui API serta distribusi event, dengan pengujian perubahan skema, ketahanan layanan, dan hak akses client.

Panduan berikut menjalankan sistem dari clone baru dan mengulang skenario P1–P5 dalam [laporan M1](docs/report/IF4031_M1_DikejarDeadline.pdf). ID dan waktu pada data sintetis berubah setiap sesi; hasil latency dan throughput bergantung pada mesin pengujian.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Arsitektur

![Diagram arsitektur sistem dari laporan M1](docs/assets/report-architecture.png)

Panah hitam menunjukkan arah request HTTP/SQL, sedangkan panah berwarna menunjukkan distribusi event melalui NATS. Third Consumer bersifat opsional dan digunakan untuk menguji penambahan consumer pada P5.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Menjalankan aplikasi

Gunakan Linux atau WSL2 dengan Bash, Git, Docker yang sudah aktif, Docker Compose v2, Python 3, OpenSSL, curl, jq, dan `ss` (paket `iproute2`). Go 1.23 atau lebih baru diperlukan untuk pengujian kode dan JSON Schema. Service dibangun melalui Docker; Python tidak memerlukan paket tambahan.

### Konfigurasi awal

```bash
git clone https://github.com/ryonlunar/tugas-besar-AAT.git
cd tugas-besar-AAT
test -e .env || cp .env.example .env
```

Buat secret berbeda untuk setiap sumber, service internal, dan client. Command berikut hanya mengisi nilai yang masih kosong:

```bash
for name in BMKG_API_KEY PVMBG_TOKEN AGGREGATOR_API_KEY CONSUMER_DEMO_KEY \
  POSTGRES_PASSWORD NATS_TOKEN MEDIA_CLIENT_SECRET FIELD_TEAM_CLIENT_SECRET \
  BNPB_INTERNAL_CLIENT_SECRET; do
  value=$(openssl rand -hex 32)
  sed -i "s/^${name}=$/${name}=${value}/" .env
done
```

Buat pasangan kunci RSA untuk signing di Auth dan verifikasi di Client API, lalu muat ke volume Docker:

```bash
set -a
. ./.env
set +a
mkdir -p .secrets
chmod 700 .secrets
if [ ! -e .secrets/jwt-private.pem ] && [ ! -e .secrets/jwt-public.pem ]; then
  (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out .secrets/jwt-private.pem)
  openssl pkey -in .secrets/jwt-private.pem -pubout -out .secrets/jwt-public.pem
fi
python3 scripts/setup-jwt-keys.py
docker compose up --build --detach --wait --wait-timeout 180
docker compose ps
curl -fsS "http://127.0.0.1:${API_HTTP_PORT:-8080}/ready"
```

Sembilan service utama harus berstatus healthy. Endpoint `/ready` pada Client API harus mengembalikan HTTP 200. PostgreSQL, NATS, dan consumer hanya tersedia di jaringan container.

| Service yang dapat diakses dari host | Alamat default |
|---|---|
| Client API | `http://127.0.0.1:8080` |
| BMKG mock | `http://127.0.0.1:8081` |
| PVMBG mock | `http://127.0.0.1:8082` |
| Aggregator | `http://127.0.0.1:8083` |
| Auth | `http://127.0.0.1:8084` |

### Environment untuk pengujian

Jalankan blok ini sekali. Lanjutkan semua command pengujian di terminal yang sama agar environment tetap tersedia:

```bash
set -o pipefail
set -a
. ./.env
set +a
export API_URL="http://127.0.0.1:${API_HTTP_PORT:-8080}"
export AUTH_URL="http://127.0.0.1:${AUTH_HTTP_PORT:-8084}"
export BMKG_URL="http://127.0.0.1:${BMKG_MOCK_HTTP_PORT:-8081}"
export PVMBG_URL="http://127.0.0.1:${PVMBG_MOCK_HTTP_PORT:-8082}"
EVIDENCE_DIR="docs/evidence/private/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$EVIDENCE_DIR"
```

Hasil baru disimpan di `$EVIDENCE_DIR`, terpisah dari bukti pengukuran yang sudah ada di laporan. Jalankan skenario secara berurutan; beberapa pengujian mengubah kontrol mock atau menghentikan service sementara.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Variabel konfigurasi

[.env.example](.env.example) memuat seluruh nilai default. Pertahankan profil berikut untuk mengulang pengujian laporan: seed 20 record, event baru setiap 10 detik, polling 5 detik, PVMBG skema 1 tanpa outage, dan access token 60 detik. P2 mengubah delay PVMBG menjadi 3000 ms selama load test.

| Kelompok | Variabel | Fungsi |
|---|---|---|
| Project | `COMPOSE_PROJECT_NAME` | Nama container, network, dan volume; default `aat` |
| Port HTTP | `BMKG_MOCK_HTTP_PORT`, `PVMBG_MOCK_HTTP_PORT`, `API_HTTP_PORT`, `AGGREGATOR_HTTP_PORT`, `AUTH_HTTP_PORT`, `DASHBOARD_HTTP_PORT`, `NOTIFIER_HTTP_PORT` | Listener service; default 8081, 8082, 8080, 8083, 8084, 8085, 8086 |
| Alamat HTTP internal | `BMKG_BASE_URL`, `PVMBG_BASE_URL`, `AGGREGATOR_BASE_URL` | Tujuan polling dan pembacaan internal |
| Credential sumber/internal | `BMKG_API_KEY`, `PVMBG_TOKEN`, `AGGREGATOR_API_KEY`, `CONSUMER_DEMO_KEY` | Autentikasi mock, API internal, dan endpoint demo consumer |
| Credential client | `MEDIA_CLIENT_SECRET`, `FIELD_TEAM_CLIENT_SECRET`, `BNPB_INTERNAL_CLIENT_SECRET` | Login `media`, `field-team`, dan `bnpb-internal` |
| PostgreSQL | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_SSLMODE` | Koneksi Canonical Store |
| Mock | `MOCK_SEED_COUNT`, `MOCK_EVENT_INTERVAL`, `BMKG_DELAY_MS`, `PVMBG_DELAY_MIN_MS`, `PVMBG_DELAY_MAX_MS`, `PVMBG_SCHEMA_VERSION`, `PVMBG_OUTAGE_ENABLED` | Seed, interval event, delay, skema awal, dan outage awal |
| Polling | `SOURCE_POLL_INTERVAL`, `BMKG_REQUEST_TIMEOUT`, `PVMBG_REQUEST_TIMEOUT`, `SOURCE_RESPONSE_MAX_BYTES`, `SOURCE_STALE_AFTER` | Interval, deadline, ukuran snapshot, dan ambang stale |
| Token | `ACCESS_TOKEN_TTL`, `REFRESH_TOKEN_TTL`, `JWT_ISSUER`, `JWT_AUDIENCE`, `JWT_PRIVATE_KEY_FILE`, `JWT_PUBLIC_KEY_FILE` | Masa berlaku, claim, dan lokasi kunci JWT |
| State lokal | `AUTH_STATE_PATH`, `DASHBOARD_STATE_PATH`, `NOTIFIER_STATE_PATH` | Lokasi SQLite privat masing-masing service |
| Broker | `NATS_URL`, `NATS_TOKEN`, `NATS_STREAM_NAME`, `NATS_DASHBOARD_DURABLE`, `NATS_NOTIFIER_DURABLE` | Koneksi NATS, stream, dan durable independen |

<details>
<summary>Konfigurasi kapasitas, timeout, dan retry</summary>

| Kelompok | Variabel | Fungsi |
|---|---|---|
| Pool PostgreSQL | `DB_READ_MAX_CONNS`, `DB_BMKG_WRITE_MAX_CONNS`, `DB_PVMBG_WRITE_MAX_CONNS`, `DB_RELAY_MAX_CONNS`, `DB_READ_TIMEOUT`, `DB_WRITE_TIMEOUT` | Memisahkan kapasitas baca, ingest, dan relay serta membatasi durasi operasi |
| API dan HTTP | `API_MAX_INFLIGHT`, `AGGREGATOR_MAX_INFLIGHT`, `AGGREGATOR_REQUEST_TIMEOUT`, `HTTP_READ_HEADER_TIMEOUT`, `HTTP_IDLE_TIMEOUT`, `HTTP_WRITE_TIMEOUT` | Admission limit dan timeout HTTP |
| Auth | `AUTH_MAX_INFLIGHT`, `AUTH_BODY_MAX_BYTES`, `AUTH_READ_TIMEOUT` | Batas request bersamaan, ukuran body, dan waktu baca |
| Retention JetStream | `NATS_MAX_AGE`, `NATS_MAX_BYTES`, `NATS_MAX_MSGS`, `NATS_MAX_MSG_SIZE`, `NATS_DUPLICATE_WINDOW` | Batas penyimpanan dan window dedup; default 24 jam, 256 MiB, 100.000 pesan, 64 KiB, dan 2 menit |
| Publish dan consumer | `NATS_PUBLISH_TIMEOUT`, `NATS_ACK_WAIT`, `NATS_MAX_ACK_PENDING`, `NATS_MAX_DELIVER`, `NATS_FETCH_BATCH`, `NATS_FETCH_EXPIRES`, `CONSUMER_NAK_DELAY` | Deadline publish, ACK, batch pull, dan redelivery |
| Outbox | `OUTBOX_POLL_INTERVAL`, `OUTBOX_BATCH_SIZE`, `OUTBOX_MAX_PENDING`, `OUTBOX_RETRY_MIN`, `OUTBOX_RETRY_MAX` | Interval relay, ukuran batch, kapasitas antrean, dan backoff retry |

</details>

Jika port service berubah, sesuaikan base URL internalnya. Compose menetapkan alamat PostgreSQL/NATS serta path kunci dan state; nilai variabel tersebut di `.env.example` juga menjadi acuan saat menjalankan binary langsung.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Pengujian per problem

<a id="problem-1"></a>

### P1: Arsitektur & Interoperabilitas Data

Uji pemetaan dan kontrak data, lalu jalankan trial pada sistem aktif:

```bash
go test ./contracts ./services/aggregator/internal/mapper/...
python3 scripts/verify-integration.py --scenario smoke --output "$EVIDENCE_DIR/smoke.json" \
  | tee "$EVIDENCE_DIR/smoke.log"
jq '{passed, results}' "$EVIDENCE_DIR/smoke.json"
```

Hasil yang diharapkan: `passed: true`, pemetaan BMKG/PVMBG sesuai sumber, dan record PVMBG mempunyai revisi sebelum serta sesudah penambahan `confidence_level` ke `attributes`. Trial memicu perubahan melalui `/admin/schema-version`, lalu membandingkan ID hazard, daftar migration, dan kondisi container untuk memastikan tidak ada restart atau redeploy. Skema dikembalikan ke versi awal setelah trial.

Trial `smoke` juga memeriksa outage P2 dan hak akses P3. Setiap pemeriksaan dicatat terpisah dalam `results`.

<a id="problem-2"></a>

### P2: Concurrency, Availability, & Graceful Degradation

Pada hasil `smoke`, pemeriksaan `live PVMBG outage` harus menunjukkan HTTP 200 dengan data terakhir berstatus `stale`; BMKG tetap dilayani. Trial memulihkan PVMBG dan menunggu status kembali `fresh` tanpa restart.

Untuk pengukuran beban, atur delay sumber tepat 3 detik dan ukur respons langsung dari mock:

```bash
curl -fsS -X POST "$PVMBG_URL/admin/delay" \
  -H "Authorization: Bearer $PVMBG_TOKEN" \
  -H 'Content-Type: application/json' -d '{"min_ms":3000,"max_ms":3000}'
curl -fsS -o /dev/null -w 'HTTP %{http_code}, durasi %{time_total} detik\n' \
  -H "Authorization: Bearer $PVMBG_TOKEN" "$PVMBG_URL/volcanic-reports"
sleep 5
```

Respons langsung PVMBG harus 200 dengan durasi sekitar 3 detik. Jalankan 30 worker BMKG dan 30 worker PVMBG selama 90 detik sambil mencatat koneksi TCP setiap detik:

```bash
python3 scripts/load/capture-tcp-connections.py --api-url "$API_URL" \
  --seconds 110 --interval 1 --output "$EVIDENCE_DIR/tcp.json" &
tcp_pid=$!
python3 scripts/load/hazards-monotonic.py --seconds 90 --vus-per-source 30 \
  --output "$EVIDENCE_DIR/load.json" | tee "$EVIDENCE_DIR/load.log"
load_exit=$?
wait "$tcp_pid"
tcp_exit=$?
test "$load_exit" -eq 0 && test "$tcp_exit" -eq 0
jq '{passed, profile, sources, hazard_successes_per_second, hazard_unexpected_error_percent, auth}' \
  "$EVIDENCE_DIR/load.json"
```

Hasil yang diharapkan: `passed: true`, p95 respons sukses BMKG <300 ms, error selain 429 <1%, dan kedua sumber menerima request sukses. Output memuat p50/p95/p99, throughput, serta jumlah 429 pada API dan Auth. Pengukuran menggunakan timer monotonic sampai seluruh body respons diterima.

Periksa durasi koneksi TCP dengan menghitung rangkaian sampel berurutan yang mempunyai sedikitnya 50 koneksi:

```bash
jq '.sampling_interval_seconds as $interval
  | reduce .samples[] as $s
      ({start: null, previous: null, longest: 0, peak: 0};
       .peak = ([.peak, $s.established_connections_to_api] | max)
       | if $s.established_connections_to_api >= 50 then
           if .start == null or ($s.elapsed_seconds - .previous > 2.5 * $interval)
           then .start = $s.elapsed_seconds else . end
           | .longest = ([.longest, ($s.elapsed_seconds - .start)] | max)
         else .start = null end
       | .previous = $s.elapsed_seconds)
  | {peak_connections: .peak, seconds_at_least_50: .longest, passed: (.longest >= 60)}' \
  "$EVIDENCE_DIR/tcp.json" | tee "$EVIDENCE_DIR/tcp-assessment.json"
```

`seconds_at_least_50` harus sedikitnya 60 detik dan `passed` harus `true`. Jumlah worker saja tidak cukup untuk membuktikan jumlah koneksi TCP.

Kembalikan delay setelah pengujian:

```bash
curl -fsS -X POST "$PVMBG_URL/admin/delay" \
  -H "Authorization: Bearer $PVMBG_TOKEN" \
  -H 'Content-Type: application/json' -d '{"min_ms":500,"max_ms":3000}'
```

<a id="problem-3"></a>

### P3: Service-to-Service Authentication & Trust

Bandingkan credential sumber secara langsung. Dua command pertama harus menghasilkan 200; dua berikutnya harus ditolak dengan 401/403:

```bash
curl -sS -o /dev/null -w 'BMKG dengan key BMKG: %{http_code}\n' \
  -H "X-BMKG-Key: $BMKG_API_KEY" "$BMKG_URL/seismic-events"
curl -sS -o /dev/null -w 'PVMBG dengan token PVMBG: %{http_code}\n' \
  -H "Authorization: Bearer $PVMBG_TOKEN" "$PVMBG_URL/volcanic-reports"
curl -sS -o /dev/null -w 'BMKG dengan key PVMBG: %{http_code}\n' \
  -H "X-BMKG-Key: $PVMBG_TOKEN" "$BMKG_URL/seismic-events"
curl -sS -o /dev/null -w 'PVMBG dengan token BMKG: %{http_code}\n' \
  -H "Authorization: Bearer $BMKG_API_KEY" "$PVMBG_URL/volcanic-reports"
```

Pemeriksaan `client access` pada hasil `smoke` menunjukkan Media menerima tepat tujuh field ringkasan dan ditolak saat meminta `view=full`. Field Team serta BNPB internal menerima full view. Jalankan juga tes penolakan field raw dan trial expiry/refresh:

```bash
go test ./services/client-api/... ./services/auth/...
python3 scripts/verify-integration.py --scenario tokens --output "$EVIDENCE_DIR/tokens.json" \
  | tee "$EVIDENCE_DIR/tokens.log"
jq '{passed, results}' "$EVIDENCE_DIR/tokens.json"
```

Hasil yang diharapkan: JWT yang masih valid tetap dapat dipakai saat Auth berhenti; setelah expiry alami 60 detik, token lama menerima 401. Refresh menghasilkan token baru yang menerima 200 tanpa login ulang. Token lama tetap ditolak, dan penggunaan ulang refresh token membatalkan keluarga sesi terkait. Trial menjalankan kembali Auth setelah pemeriksaan outage.

Secret dihasilkan lokal pada setup. `.env` dan kunci privat diabaikan Git; `.env.example` hanya memuat nilai secret kosong.

<a id="problem-4"></a>

### P4: Arsitektur Microservice & Flexible Storage

Periksa tipe kolom, migration, riwayat, dan outbox melalui service pemilik Canonical Store:

```bash
docker compose exec -T aggregator /aggregator store-audit \
  | jq . | tee "$EVIDENCE_DIR/store-audit.json"
docker compose config --format json \
  | jq '{store_network: .networks["store-net"],
         owners: [.services | to_entries[]
           | select(.value.networks | has("store-net")) | .key],
         postgres_ports: (.services.postgres.ports // [])}'
```

Output harus menunjukkan kolom dokumen bertipe JSONB, hanya Aggregator dan PostgreSQL berada di `store-net`, serta PostgreSQL tidak memublikasikan port ke host. Trial P1 membuktikan revisi v1/v2 tetap terbaca dengan migration yang sama.

Pemeriksaan rebuild dijalankan bersama P5 melalui skenario `events` di bawah. Hasil `consumer rebuild while API remains available` harus menunjukkan seluruh request API menerima 200 dan kondisi container lain tetap sama.

<a id="problem-5"></a>

### P5: Fan-out Event Distribution/Pub-Sub

Jalankan trial penghentian consumer, recovery, rebuild P4, dan penambahan durable baru:

```bash
python3 scripts/verify-integration.py --scenario events --output "$EVIDENCE_DIR/events.json" \
  | tee "$EVIDENCE_DIR/events.log"
jq '{passed, results}' "$EVIDENCE_DIR/events.json"
```

Hasil yang diharapkan: Notifier dan producer terus berjalan saat Dashboard berhenti; setelah Dashboard hidup kembali, ACK-nya mencapai sequence target dari periode gangguan. Consumer ketiga membaca event yang masih tersimpan tanpa perubahan container Aggregator. Trial memakai durable baru setiap kali dijalankan, lalu menghapus durable sementara dan mengembalikan Third Consumer ke konfigurasi demo.

Periksa posisi stream dan durable serta inbox lokal masing-masing consumer:

```bash
docker compose exec -T nats wget -qO- \
  'http://127.0.0.1:8222/jsz?streams=true&consumers=true' \
  | jq '.account_details[].stream_detail[] | {name, state, consumer_detail}'
docker compose exec -T dashboard-consumer /dashboard-consumer inspect | jq .
docker compose exec -T notifier-consumer /notifier-consumer inspect | jq .
docker compose exec -T third-consumer /notifier-consumer inspect | jq .
```

Durable memiliki posisi ACK sendiri. Inbox setiap consumer harus terisi dan `quarantine_count` harus nol pada data mock normal. Recovery dibuktikan dengan ACK yang melewati target gangguan; `pending: 0` saja belum cukup karena masih mungkin ada pesan menunggu ACK.

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Log dan menghentikan sistem

Untuk menelusuri request trial lewat correlation ID:

```bash
docker compose logs --no-color aggregator client-api auth bmkg-mock pvmbg-mock \
  | grep 'm1-integration-trial' | tee "$EVIDENCE_DIR/correlation.log"
```

Hentikan seluruh stack, termasuk consumer demo, dengan tetap menyimpan volume data:

```bash
docker compose --profile demo down
```

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>

---

## Lokasi kode dan laporan

| Lokasi | Isi |
|---|---|
| [services/](services/) | Mock, Aggregator, Auth, Client API, dan consumer; masing-masing memiliki Dockerfile |
| [contracts/](contracts/README.md) | JSON Schema dan contoh data |
| [docker-compose.yml](docker-compose.yml) | Container, network, dan volume |
| [scripts/](scripts/README.md) | Runner integrasi, load test, dan utilitas pengujian |
| [docs/report/](docs/report/README.md) | PDF, sumber LaTeX, dan bukti pengukuran laporan |

<div align="right"><a href="#readme-top">↖ Kembali ke atas</a></div>
