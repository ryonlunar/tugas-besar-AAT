# Catatan Observasi Feed Satelit (Task 0.3)

Diobservasi langsung dari `https://satellite.naufarrel.dev` pada 18 November 2026 menggunakan
`scripts/m2/probe-satellite.py` (token dibaca dari `.secrets/satellite-secret.yaml`, tidak pernah
dicetak). Maksimal 1 koneksi SSE aktif pada satu waktu (sesuai batas "jangan buka >2 koneksi").
`POST /v1/exam/start` **tidak** dipanggil.

Hasil mentah (byte SSE asli + ringkasan JSON, **tidak berisi token**) ada di
`.state/m2/satellite-probe/` (di-ignore Git).

## 1. `GET /v1/session`

```json
{"profile":"practice","speed":60,"group":"dikejardeadline","latest_offset":0,"virtual_now":"2026-11-21T00:00:00Z"}
```

- Profil latihan kelompok berjalan pada **speed 60** (60×) bahkan sebelum ada koneksi stream — offset
  dan `virtual_now` terus maju di background begitu sesi aktif, bukan hanya saat ada klien yang membaca.
  Dua menit kemudian (setelah ~1 menit koneksi stream + beberapa request lain) `latest_offset` sudah
  1365 dan `virtual_now` maju ke `2026-11-21T03:14:28Z` (≈3 jam 14 menit virtual dalam ~3 menit nyata —
  konsisten dengan 60×).
- **Implikasi:** adapter yang terlambat connect atau sempat disconnect lama akan punya backlog besar;
  `CanRead`/backpressure (D4) dan target catch-up ≤60 s (P4.4b) harus diuji dengan asumsi ini.

## 2. Semantik `from` vs `Last-Event-ID` — **BERBEDA, sudah diverifikasi langsung**

| Resume via | Event pertama yang dikirim ulang |
|---|---|
| `?from=10` | **offset 10** (inklusif) |
| Header `Last-Event-ID: 10` | **offset 11** (eksklusif — standar SSE: "offset terakhir yang sudah saya terima") |

**Keputusan (mengganti D4 yang masih terbuka):** adapter **selalu** resume lewat parameter query
`?from=<cursor>`, **tidak pernah** lewat header `Last-Event-ID`. `Batch.Cursor` disimpan sebagai
"offset berikutnya yang belum pernah di-commit" (= offset terakhir yang sukses + 1), sehingga
`?from=<cursor>` pada percobaan berikutnya tepat memberi event pertama yang belum pernah diproses.
Ini menghapus ambiguitas yang masih ditandai "sesuai semantik from (Task 0.3)" di D4/Task 2.4.

## 3. Bentuk payload per jenis event — **BERBEDA dari asumsi draf awal plan**

Draf Task 2.2 (sebelum observasi) mengasumsikan field deteksi langsung di root `data`. **Faktanya
`data` adalah wrapper**, dan `update`/`retract` **tidak** mengirim objek deteksi penuh — hanya referensi
`key` + perubahan parsial. Ini mengubah desain `satellite/event.go` dan logika merge di recognizer (Fase 4).

### `event: detection`

```json
{"offset":0,"detection":{
  "version":"2.0NRT","scan":0.5,"acq_date":"2026-11-20","acq_time":2357,
  "bright_ti4":348.53,"bright_ti5":294.61,"confidence":"n","daynight":"D","frp":10.2,
  "instrument":"VIIRS","latitude":1.33702,"longitude":98.25313,"satellite":"N","track":0.56}}
```

- `id:` SSE == `offset` di body (redundan, dipakai untuk validasi silang di adapter: `id != offset` →
  `ErrOffsetGap`-setara, tolak batch).
- `acq_time` adalah **JSON number** (`2357`), bukan string — tetapi **tanpa leading zero** (JSON tidak
  mengizinkan `0243`). Ini **cocok** dengan contoh `detection_keys` di Lampiran spesifikasi
  (`"N20|2026-11-12|243|..."` — "243" bukan "0243"). Keputusan D8 (ambil token leksikal via
  `json.Decoder.UseNumber`, jangan lewat `float64`) **tetap berlaku** dan sudah benar — hanya
  perlu dikonfirmasi: `DetectedAt` (Task 3.1) tetap zero-pad dari KIRI untuk *menghitung* waktu asli
  (`"243"` → `02:43`), tetapi `detection_key` memakai token asli `"243"` tanpa padding (sudah sesuai
  rencana, tidak perlu diubah).
- `latitude`/`longitude` adalah **JSON number** (`1.33702`), bukan string. `json.Number` via
  `UseNumber()` tetap menyimpan token desimal persis (mis. `"98.25313"`) tanpa melalui `float64` —
  keputusan D8 tetap valid tanpa perubahan kode, hanya menghapus cabang "bila dikirim sebagai string"
  di Task 3.2 karena pada kenyataannya ia selalu JSON number pada payload yang teramati.
- `confidence` teramati `"n"` dan `"l"` — selaras `l|n|h` yang sudan diasumsikan Task 3.3.
- `satellite` teramati `"N"` (bukan hanya `"N20"`/`"N21"`) di salah satu sampel, berdampingan dengan
  `"N20"` pada sampel lain dalam capture yang sama. Diperlakukan sebagai string opak — tidak ada
  validasi daftar satelit tertutup di kode.
- Field lain (`version`, `scan`, `bright_ti4`, `bright_ti5`, `daynight`, `instrument`, `track`) **tidak**
  dipakai oleh Definisi Kebakaran — disimpan mentah di `Detection.Extra` (map, tidak divalidasi) untuk
  kemungkinan dibutuhkan laporan, tidak memengaruhi clustering/severity.

### `event: update` — **payload parsial, bukan deteksi penuh**

```json
{"offset":55,"key":"N20|2026-11-20|2027|1.34157|98.25263","changes":{"bright_ti4":344.23,"confidence":"l"}}
```

- `key` = `detection_key` (format sama dengan D8) yang dirujuk — **bukan** `source_ref_id`/`hazard_id`.
- `changes` hanya memuat field yang berubah (di sampel ini hanya `bright_ti4` dan `confidence`).
- **Keputusan (baru, menggantikan asumsi Task 2.2 lama):** `EventParser` menghasilkan
  `Update{Key string, Changes map[string]json.RawMessage}`; recognizer (Fase 4) **mem-merge** field
  yang ada di `Changes` ke atas `StoredDetection` yang sudah tersimpan untuk `Key` tersebut (field yang
  tidak disebut tetap seperti semula), lalu mengevaluasi ulang confidence/keanggotaan. Update ke
  `confidence:"l"` dapat membuat deteksi dikeluarkan dari graf aktif (FIRE.3) — ini **sudah** konsisten
  dengan skenario `TestRecognizerExcludesLowConfidence`/`TestRecognizerUpdateChangesFRPKeepsID` yang
  sudah direncanakan; yang berubah hanya *bentuk input*-nya (parsial by-key, bukan dokumen penuh).
- Update untuk `key` yang belum pernah dilihat (belum ada `detection` sebelumnya) → deteksi dibuat dari
  `changes` saja (field yang tidak disebut kosong/default) + catat counter `update_unknown_key` — kasus
  tepi baru yang perlu test eksplisit (`TestRecognizerUpdateUnknownKeyCreatesPartialDetection`).

### `event: retract`

```json
{"offset":63,"reason":"false_positive","key":"N21|2026-11-21|9|1.33993|98.25168"}
```

- `key` = `detection_key` yang ditarik. `reason` adalah teks bebas (`false_positive` teramati) —
  **tidak** memengaruhi logika (retract selalu berarti "keluarkan dari aktif"), disimpan sebagai
  metadata di `satellite_rejects`/log untuk laporan, bukan untuk percabangan logika.
- Retract atas `key` yang tidak dikenal → tombstone + counter `retract_unknown` (sudah direncanakan
  di Task 4.5, tidak berubah).

### `event: heartbeat` — **tidak teramati dalam jendela observasi**

- Tidak ada baris `event: heartbeat` maupun baris komentar SSE (`:`) muncul sama sekali dalam capture
  220 event / ~90 detik yang penuh traffic deteksi. Kemungkinan heartbeat hanya dikirim saat benar-benar
  idle (tidak ada deteksi untuk dikirim), yang tidak terjadi selama jendela observasi ini.
- **Keputusan:** adapter tetap menangani `event: heartbeat` secara defensif sesuai kontrak spesifikasi
  (abaikan sebagai data, reset idle timer, dan bila `id` ada ikut memajukan cursor lewat
  `Batch{Records:nil}` — lihat Task 2.5) **tanpa bisa memverifikasi bentuk body sebenarnya dari observasi
  ini**. Tandai di laporan sebagai asumsi yang belum terverifikasi langsung (bukan fakta).

## 4. Offset & urutan

- `id:` SSE monoton naik tanpa celah pada jendela yang diobservasi (`first_id=0, last_id=219`,
  `non_contiguous_gaps=[]` untuk 220 event). Konsisten dengan kontrak "offset naik monoton tanpa celah".

## 5. Error handling (terverifikasi langsung)

| Kondisi | Status | Body |
|---|---|---|
| Token salah | 401 | `{"error":{"code":"unauthorized","message":"token tidak valid"}}` |
| `from=abc` (bukan integer) | 400 | `{"error":{"code":"bad_request","message":"parameter from tidak valid"}}` |

Format error konsisten dengan Lampiran A (dipakai juga di Audit API). **Belum diuji langsung**: 429 (dua
koneksi paralel — sengaja tidak dipaksakan untuk menghindari melanggar batas "jangan buka >2 koneksi"
dan karena backoff Task 2.3 sudah menangani 429 secara generik berdasarkan status code, tidak
berdasarkan body spesifik), 503.

## 6. Distribusi keterlambatan (D7 — `SATELLITE_ALLOWED_LATENESS`)

Dalam jendela observasi 90 detik / 220 event, **tidak ada** deteksi yang tiba dengan `detected_at` di
bawah watermark per-satelit yang sedang berjalan (`lateness-by-satellite: {}`) — seluruh deteksi yang
diobservasi tiba dalam urutan waktu yang naik per satelit. Ini **tidak membuktikan** bahwa feed tidak
pernah mengirim deteksi terlambat (jendela observasi singkat, mode practice mungkin belum memasuki
skenario keterlambatan); `SATELLITE_ALLOWED_LATENESS=12h` (D7) **tetap berstatus hipotesis**, bukan nilai
yang tervalidasi dari data. Observasi lebih lama (jam, bukan menit) diperlukan sebelum laporan dapat
mengklaim nilai ini final — ditandai sebagai pekerjaan lanjutan, bukan diselesaikan di sini.

## 7. Dampak ke rencana implementasi

- **D4 (resume):** diselesaikan — selalu pakai `?from=<cursor_berikutnya>`, jangan pakai
  `Last-Event-ID`.
- **Task 2.2 (`satellite/event.go`):** struktur ulang — `ParseFeedEvent` mengembalikan salah satu dari
  `Detection` (lengkap), `Update{Key, Changes}` (parsial), atau `Retract{Key, Reason}`, bukan tiga
  bentuk deteksi yang sama.
- **Fase 4 (recognizer):** `update` **mem-merge** ke `StoredDetection` tersimpan (field baru:
  `TestRecognizerUpdateMergesPartialChanges`, `TestRecognizerUpdateUnknownKeyCreatesPartialDetection`),
  bukan menggantikan dengan dokumen deteksi baru yang lengkap.
- **Task 3.2 (`Detection.Key()`):** tidak berubah secara kode, hanya catatan bahwa lat/lon selalu JSON
  number pada payload nyata (cabang "string" tetap aman dipertahankan sebagai pertahanan berlapis, tidak
  wajib diuji dengan fixture string).
- **Heartbeat:** desain defensif tetap seperti rencana, ditandai belum terverifikasi bentuknya.
- **K5 (radius bumi) & K4 (tabel provinsi):** belum terjawab oleh observasi ini — tetap pertanyaan ke
  asisten, tidak berubah.
