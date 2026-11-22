# Verifikasi Milestone 2

Mulai dari [deployment pada README](../README.md#menjalankan-sistem). Gunakan klaster latihan dengan disk yang boleh diganti untuk skenario kehilangan node. Jalankan skenario satu per satu dari kondisi sehat. Untuk ujian gabungan, jalankan skenario yang diberikan asisten dan amati pemulihan selama window pengujian.

## Terminal pengujian

Jalankan dari direktori aplikasi di control plane. Variabel dan fungsi berikut dipakai pada langkah P1–P5. Gunakan satu direktori hasil per sesi.

```bash
cd ~/aat
source scripts/m2/demo-env.sh
```

Token client berlaku 60 detik. Ambil token baru sebelum rangkaian permintaan yang panjang. Tampilkan status, waktu, body, dan correlation ID selama merekam. Simpan hasil pada direktori bukti dan jaga berkas kredensial tetap privat.

## Tester Agent

Jalankan dari control plane setelah aplikasi terpasang. Target Make menggunakan manifest resmi pada revisi yang ditentukan spesifikasi. Jika VM sudah disiapkan dengan versi sebelumnya dan Make belum tersedia, jalankan `sudo apt-get install -y make`.

```bash
make tester-install
make tester-list
```

Pastikan satu pod Ready pada setiap node, termasuk control plane. Daftar saat runtime menentukan nama skenario yang tersedia. Skenario latihan bisa dijalankan tanpa key.

```bash
make tester-run SCENARIO=kill-leader | tee "$EVIDENCE/tester-kill-leader.txt"
kubectl -n tester-agent logs -l app=tester-agent --prefix --since=2m -f
# Hentikan tampilan log setelah aksi dan pemulihan akhir tercatat.
make -s tester-logs >"$EVIDENCE/tester-actions.log"
```

Pertahankan image dan izin pada manifest asli. Service internal `pvmbg-tester` menyediakan kontrol mock yang dipanggil Agent melalui jaringan node. Jalur data dan admin pada Service `pvmbg-mock` memakai Bearer token. Jalankan kembali `make tester-install` setelah mengganti node agar aturan alamat host mengikuti topologi klaster. Pada uji perubahan jam, simpan log lengkap dan urutkan aksi menggunakan nilai `seq` dari pod yang menjalankan skenario.

Untuk ujian, isi Secret `tester-key` sesuai format yang diberikan asisten dan gunakan perintah skenario dari instruksi ujian. Manifest memasang Secret di `/var/run/tester`. Rekam log Agent selama gangguan berlangsung.

## P1 Penyimpanan dan analitik

1. Salin katalog ke `.state/m2/bmkg-catalog.csv` sebelum deploy. Deployment memasukkannya ke image Aggregator. Catat beban tiap node selama impor dan saat uji lonjakan regional. Jalankan `kubectl top nodes` berkala dari terminal kedua. Untuk latihan distribusi dengan satu wilayah, jalankan `make foundation-check`. [Patch fixture 500 baris](report-m2/evidence/bmkg/import-500.patch) dapat diterapkan pada salinan checkout untuk mengulang ukuran uji pada laporan.
2. Simpan penempatan primary dan replica serta hasil Audit. Untuk memeriksa kursor, panggil halaman pertama dan lanjutkan memakai `meta.next_cursor`. Periksa urutan `occurred_at` dan ID sebagai tie-breaker.

   ```bash
   k get pods -l role=store -o wide -L cluster-name,postgres-role | tee "$EVIDENCE/p1-placement.txt"
   for shard in shard-0 shard-1 shard-2 shard-3; do
     primary=$(k get pod -l "cluster-name=$shard,postgres-role=master" -o jsonpath='{.items[0].metadata.name}')
     printf '%s ' "$shard"
     k exec "$primary" -- psql -U postgres -d aat -Atc 'SELECT count(*) FROM hazard_events;'
   done | tee "$EVIDENCE/p1-shard-counts.txt"
   make pipeline-check | tee "$EVIDENCE/p1-audit.txt"
   curl -fsS -H "Authorization: Bearer $(field_login)" "$API_URL/v1/hazards?view=full&limit=50" >"$EVIDENCE/p1-page1.json"
   cursor=$(jq -er '.meta.next_cursor' "$EVIDENCE/p1-page1.json")
   curl -fsS -G -H "Authorization: Bearer $(field_login)" --data-urlencode "cursor=$cursor" \
     "$API_URL/v1/hazards?view=full&limit=50" >"$EVIDENCE/p1-page2.json"
   ```

3. Siapkan terminal Audit dengan `k port-forward service/aggregator 18083:8083`. Pada terminal pengujian, ambil checksum sebelum dan sesudah gangguan, lalu bandingkan dengan referensi asisten untuk dataset yang sama.

   ```bash
   audit() {
     curl -fsS --max-time 10 -H "Authorization: Bearer $(jq -er '.AUDIT_TOKEN' "$CREDS")" \
       "http://127.0.0.1:18083/internal/audit/$1"
   }
   audit checksum | tee "$EVIDENCE/p1-checksum-before.json"
   ```

4. Jalankan `make tester-run SCENARIO=kill-store` untuk penggantian pod. Gunakan `make tester-run SCENARIO=wipe-node` hanya pada klaster yang disiapkan untuk uji hapus disk. Pastikan setiap primary mempunyai replica pada node lain sebelum mulai. Saat pemulihan, ulangi `amati '/v1/hazards?view=full&limit=50'` dan simpan kode HTTP, waktu, serta `meta.partial`. Setelah worker kosong bergabung pada slot yang hilang, jalankan `make cluster-check`. Pada Multipass, `make up` dari host menyiapkan node bernama sama setelah VM lama dilepas. Simpan kembali penempatan dan `audit checksum` setelah seluruh shard memiliki tiga salinan.
5. Buka `$API_URL/analytics/` pada browser dan gunakan token client. Bandingkan agregat dengan Audit pada rentang waktu UTC yang sama. Catat coverage saat satu unit tidak terjangkau, serta tren kategori, severity, dan wilayah.

## P2 Koordinasi Aggregator

1. Jalankan `k get pods -l app=aggregator -o wide` untuk merekam tiga replika pada tiga worker. Sebelum dan sesudah `make tester-run SCENARIO=kill-leader`, rekam log BMKG dan PVMBG. Pastikan pod yang terkena memang pemilik aktif dengan membandingkan `aggregator_instance` pada log upstream dan aksi Tester Agent. Jika skenario memilih replika lain, gunakan log untuk memilih pemilik aktif dalam uji penggantian pod yang terpisah.

   ```bash
   k logs deployment/bmkg-mock --since=2m >"$EVIDENCE/p2-bmkg.log"
   k logs deployment/pvmbg-mock --since=2m >"$EVIDENCE/p2-pvmbg.log"
   make -s tester-logs >"$EVIDENCE/p2-tester.log"
   ```

   Untuk uji jam, gunakan `make tester-run SCENARIO=clock-skew` dan rekam offset aktual. Uji +45 s dan −30 s pada node pemilik jika skenario latihan memakai offset lain. Untuk freeze, jalankan `multipass suspend "$OWNER_NODE"` dari host untuk VM pemilik, tunggu lebih dari lease TTL, lalu jalankan `multipass start "$OWNER_NODE"`. Bandingkan waktu polling, caller, dan urutan ID. `make foundation-check` menyertakan `TestExpiredOwnerCannotAppendAfterTakeover` untuk memeriksa penolakan append dari sesi lama.

2. Ikuti [runner k6](../scripts/README.md#load-test-m2-p2) pada host terpisah. Siapkan tiga Aggregator. Setelah `active-vus.jsonl` menunjukkan 10.000 VU aktif, jalankan perubahan berikut dalam plateau. Sisakan waktu untuk kedua perubahan sebelum plateau berakhir.

   ```bash
   date -u -Is
   k scale deployment/aggregator --replicas=5
   k rollout status deployment/aggregator
   date -u -Is
   k scale deployment/aggregator --replicas=2
   k rollout status deployment/aggregator
   date -u -Is
   ```

   Simpan `active-vus.jsonl`, `result.json`, waktu perubahan, dan bukti satu pemilik per sumber. Targetnya minimal 120 s pada 10.000 VU dan galat tak terduga di bawah 1%, di luar penolakan terkontrol.

3. Jalankan dua pengukuran throughput dengan `P2_RATE` dan `P2_SECONDS` yang sama. Gunakan `k scale deployment/aggregator --replicas=1` untuk baseline, lalu tiga untuk pembanding. Tunggu rollout sebelum masing-masing pengukuran dan gunakan direktori hasil terpisah pada runner. Catat HTTP 200, respons lengkap, respons parsial, dan 429 secara terpisah.
4. Siapkan dua image dari revisi yang berbeda. Uji laporan memakai `13c638c` sebagai versi sebelumnya. Simpan image dari deployment rilis yang sedang berjalan sebagai tujuan. Di control plane, siapkan dan pasang versi sebelumnya lebih dulu.

   ```bash
   NEW_AGGREGATOR_IMAGE=$(k get deployment/aggregator -o jsonpath='{.spec.template.spec.containers[?(@.name=="aggregator")].image}')
   OLD_AGGREGATOR_IMAGE=aat.local/aggregator:m2-13c638c
   previous=$(mktemp -d)
   git clone --quiet --no-checkout https://github.com/ryonlunar/tugas-besar-AAT.git "$previous/repo"
   mkdir "$previous/src"
   git -C "$previous/repo" archive 13c638c go.mod go.sum .dockerignore contracts pkg services |
     tar -xf - -C "$previous/src"
   sudo buildah bud --layers --format docker --isolation chroot --network host \
     -t "$OLD_AGGREGATOR_IMAGE" -f "$previous/src/services/aggregator/Dockerfile" "$previous/src"
   sudo buildah push --tls-verify=false "$OLD_AGGREGATOR_IMAGE" docker://aat-control:30500/aggregator:m2-13c638c
   k set image deployment/aggregator aggregator="$OLD_AGGREGATOR_IMAGE"
   k rollout status deployment/aggregator
   ```

   Mulai k6 dari host terpisah. Saat beban stabil, jalankan `k set image deployment/aggregator aggregator="$NEW_AGGREGATOR_IMAGE"` dan `k rollout status deployment/aggregator`. Simpan digest sebelum dan sesudah, waktu pergantian, dan jumlah permintaan gagal. Akhiri uji dengan tiga Aggregator versi rilis.

## P3 Evolusi skema dan laporan

Buka terminal dengan `k port-forward service/pvmbg-mock 18082:8082`. Pada terminal pengujian, definisikan perintah admin berikut. Uji mode satu per satu dan tunggu satu polling untuk setiap perubahan.

```bash
pvmbg_mode() {
  curl -fsS -H "Authorization: Bearer $(jq -er '.PVMBG_TOKEN' "$CREDS")" \
    -H 'Content-Type: application/json' -d "$1" http://127.0.0.1:18082/admin/schema-version
}
pvmbg_mode '{"mode":"rename_field"}'
# Ulangi untuk {"mode":"type_change"} dan {"mode":"missing_required_field"}.
curl -fsS -H "Authorization: Bearer $(ops_login)" \
  "$API_URL/failed-messages?source=PVMBG" | tee "$EVIDENCE/p3-parked.json"
```

Gunakan versi 2 untuk perubahan kompatibel dan versi 1 untuk kembali ke baseline. Pada jalur Ops, ambil detail `GET /failed-messages/<id>` dan simpan byte asal, alasan, source, dan waktu. Ulangi request yang sama dengan `media_login` dan `field_login` untuk memeriksa 403. Jalankan `make foundation-check` untuk masukan rusak dan percobaan ulang, lalu `make pipeline-recovery` untuk penyimpanan raw dan pemulihan receiver.

Uji replay data PVMBG yang diparkir dengan adapter yang sesuai. Aktifkan adapter, tunggu Aggregator siap dan kembali memiliki sumber PVMBG, lalu isi `MESSAGE_ID` dengan ID dari catatan paling awal dalam urutan jurnal.

```bash
k set env deployment/aggregator PVMBG_SCHEMA_ADAPTERS=level_alias,eruption_count_string,ash_height_optional
k rollout status deployment/aggregator
curl -fsS -X POST -H "Authorization: Bearer $(ops_login)" "$API_URL/failed-messages/$MESSAGE_ID/replay"
# Setelah memeriksa hasil replay, pulihkan konfigurasi.
pvmbg_mode '{"version":1}'
k set env deployment/aggregator PVMBG_SCHEMA_ADAPTERS-
k rollout status deployment/aggregator
```

Untuk laporan Tim Lapangan, buka [langkah uji receipt](m2-field-reports.md#manual-demo). Pause worker pemrosesan sebelum POST, simpan 202 dan `report_id`, lalu ganti pod penerima yang ditemukan melalui log `field report accepted`. Periksa receipt masih ada, kirim ulang dengan key dan body identik, aktifkan pemrosesan kembali, lalu pastikan status `processed` dan satu percobaan pemrosesan.

## P4 Feed Satelit

Aktifkan sumber melalui [konfigurasi Feed Satelit](../infra/k8s/README.md#multi-node-profile). Konfirmasi sesi dan simpan posisi awal. Perintah ini mengasumsikan Secret lokal tersedia pada control plane.

```bash
export SATELLITE_TOKEN="$(python3 scripts/m2/probe-satellite.py --read-secret .secrets/satellite-secret.yaml)"
python3 scripts/m2/probe-satellite.py --session | tee "$EVIDENCE/p4-session.json"
unset SATELLITE_TOKEN
k logs -l app=aggregator --since=3m --prefix --max-log-requests=10 | grep -i satellite
amati '/v1/hazards?source=SATELIT&view=full&limit=10' | tee "$EVIDENCE/p4-events.txt"
```

Ambil owner dari log `source ownership acquired` untuk SATELIT dan hapus pod itu dengan `k delete pod "$SATELLITE_OWNER"` pada latihan. Bandingkan offset terakhir dan backlog sebelum serta sesudah sambungan pulih. Untuk lonjakan 60×, rekam saat lonjakan selesai, hitung waktu sampai lag dan backlog kembali nol, dan simpan histogram p95. Cocokkan offset dan hasil kebakaran dengan referensi asisten. Gunakan fixture di `check-foundation.py` untuk pengelompokan, koreksi, keterlambatan dan pemulihan state pada input terkendali.

Pada sesi ujian, gunakan kredensial dan referensi yang diterbitkan asisten. Mulai Feed Satelit dengan `POST /v1/exam/start` saat perekaman dimulai, lalu rekam pemrosesan live selama beberapa menit.

## P5 Sensor Pantai

Aktifkan [Sensor Pantai](../infra/k8s/README.md#sensor-pantai-and-analytics) dan tunggu data masuk. Untuk latihan outage, jalankan blok berikut pada klaster dengan sensor dan cache yang telah terisi.

```bash
make -s coastal-check | tee "$EVIDENCE/p5-before.json"
k scale deployment/sensor-pantai --replicas=0
k rollout status deployment/sensor-pantai
for i in {1..5}; do
  amati '/v1/hazards?source=SENSOR_PANTAI&view=full&limit=3'
  sleep 2
done | tee "$EVIDENCE/p5-coastal.txt"
amati '/v1/hazards?source=BMKG&view=full&limit=3' | tee "$EVIDENCE/p5-bmkg.txt"
amati '/v1/hazards?source=PVMBG&view=full&limit=3' | tee "$EVIDENCE/p5-pvmbg.txt"
k scale deployment/sensor-pantai --replicas=1
k rollout status deployment/sensor-pantai
```

Periksa data cache bertanda stale atau status tidak tersedia yang eksplisit. Untuk mengukur p95 BMKG dan PVMBG, tahan sensor pada nol replika selama beban baca M1, lalu kembalikan ke satu replika. Jalankan runner berikut di control plane atau host k6 dengan alamat k3s yang dapat dijangkau.

```bash
API_URL="$API_URL" AUTH_URL="$AUTH_URL" FIELD_TEAM_CLIENT_SECRET="$(jq -er '.FIELD_TEAM_CLIENT_SECRET' "$CREDS")" \
  python3 scripts/load/hazards-monotonic.py --seconds 90 --vus-per-source 30 --output "$EVIDENCE/p5-other-sources.json"
```

Pantau laju polling selama gangguan dan status fresh setelah pulih. Pada uji overload, bandingkan HTTP 429 dan `Retry-After` untuk Media dengan keberhasilan Tim Lapangan. Untuk mengulang bukti fixture pada host dengan Go, jalankan `go test ./services/client-api/internal/api -run TestMediaOverloadIsShedWhileFieldTeamIsServed -v`.

Gunakan daftar pengamatan kritis independen dengan `available_at` sebagai waktu ketika data tersedia di sensor. Jalankan `make field-alerts-check EXPECTED=path/expected-critical.json` dan simpan `.state/m2/check-field-alerts.json`. Uji gabungan menggunakan key dan profil yang disediakan asisten. Saat window pemulihan, catat gejala, dampak, dan waktu pulih dari dashboard.

## Observabilitas

Buka Grafana di `http://<node-ip>:30000` dan Dasbor Analitik di `http://<node-ip>:30080/analytics/`. Siapkan tab metrik, CLI basis data melalui `kubectl exec`, request dengan `time_total`, dan log Tester Agent. Pada terminal pengujian, jalankan `make cluster-check` lalu baca `checks.http_database_trace.trace_id` di `.state/m2/check-cluster.json`. Gunakan trace ID itu di Grafana Explore → Tempo untuk melihat durasi lintas layanan. Cari correlation ID yang sama di log terstruktur atau Loki.

Untuk merekam video, gunakan urutan setup dan peta arsitektur, P1–P3, lalu alur live P4 dan gangguan P5 sesuai window ujian. Setiap anggota menjelaskan bagian kode, menunjukkan hasil pengukuran, dan menyebutkan batas bukti yang relevan. Simpan checksum Audit dan log ujian bersama bukti per problem.
