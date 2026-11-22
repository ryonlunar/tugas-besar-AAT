# Feed Satelit

Contoh payload dan offset berikut diamati pada sesi latihan dengan `scripts/m2/probe-satellite.py`.

## 1. Sesi

```json
{"profile":"practice","speed":60,"group":"dikejardeadline","latest_offset":0,"virtual_now":"2026-11-21T00:00:00Z"}
```

`GET /v1/session` memberikan posisi dan waktu virtual feed. Pada profil 60×, posisi tersebut terus maju saat koneksi tidak aktif sehingga backlog dapat menumpuk.

## 2. Resume dari offset

| Permintaan | Offset pertama |
| --- | --- |
| `?from=10` | 10 |
| `Last-Event-ID: 10` | 11 |

Adapter menyimpan offset berikutnya setelah batch masuk jurnal. Saat tersambung kembali, `?from=<cursor>` membaca mulai dari offset tersebut. Jika payload memiliki `offset`, nilainya harus sesuai dengan `id` SSE. Adapter memeriksa kesinambungan offset sebelum menyimpan batch.

## 3. Bentuk payload

**Detection** membawa objek lengkap di dalam field `detection`.

```json
{"offset":0,"detection":{"acq_date":"2026-11-20","acq_time":2357,"confidence":"n","frp":10.2,"latitude":1.33702,"longitude":98.25313,"satellite":"N"}}
```

`acq_time`, latitude, dan longitude dikirim sebagai angka JSON. Kunci deteksi mempertahankan representasi angka dari payload. Sebagai contoh, angka `243` menjadi bagian kunci sebagaimana diterima, sedangkan parser waktu membacanya sebagai pukul 02.43. Identitas satelit diperlakukan sebagai string. Tingkat confidence yang diterima adalah `l`, `n`, dan `h`.

**Update** merujuk kunci deteksi. Field yang disebut dalam `changes` menggantikan nilai sebelumnya. Recognizer menghitung kembali keanggotaan deteksi dalam kebakaran.

```json
{"offset":55,"key":"N20|2026-11-20|2027|1.34157|98.25263","changes":{"bright_ti4":344.23,"confidence":"l"}}
```

**Retract** menarik deteksi berdasarkan kuncinya. Alasan dicatat dan recognizer mengeluarkan deteksi dari himpunan aktif.

```json
{"offset":63,"reason":"false_positive","key":"N21|2026-11-21|9|1.33993|98.25168"}
```

**Heartbeat** menjaga koneksi saat feed tidak mengirim pengamatan. Adapter tidak memasukkan heartbeat ke pengenal kebakaran.

## 4. Kegagalan dan keterlambatan

Token yang salah menghasilkan HTTP 401. Parameter `from` yang bukan bilangan bulat menghasilkan HTTP 400. Adapter mengikuti kebijakan retry untuk kegagalan sementara.

Dalam sampel latihan 220 event, offset bergerak dari 0 sampai 219 tanpa celah dan waktu deteksi tidak mundur pada tiap satelit. `SATELLITE_ALLOWED_LATENESS` menentukan batas keterlambatan yang diterima. Nilai default 12 jam adalah pilihan rancangan. Sampel latihan tersebut tidak mengukur sebaran keterlambatan pada skenario ujian.
