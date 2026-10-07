# Laporan M1 DikejarDeadline

- PDF: `IF4031_M1_DikejarDeadline.pdf`.
- Sumber LaTeX: `main.tex`, `setup.tex`, `chapters/`, `appendices/`.
- Hasil uji: `evidence/`. Source dan gambar terminal: `figures/`.

Build dari folder ini dengan Tectonic 0.17:

```sh
tectonic main.tex
mv main.pdf IF4031_M1_DikejarDeadline.pdf
```

Gambar terminal berisi command langsung beserta output yang ditangkap pada
7 Oktober. `evidence/terminal-commands.json` mencatat command, waktu, dan exit code.
Load test dan grafik TCP tetap menggunakan pengukuran 5 Oktober.

Untuk membuat ulang satu gambar dengan Freeze 0.2.2 dan font Noto Sans Mono:

```sh
freeze figures/p1-schema.txt --config figures/freeze.json \
  --wrap 76 --output figures/p1-schema.png
```

Untuk mengambil hasil uji baru,
jalankan command yang tercantum pada gambar dari root repo dengan stack demo
aktif. Untuk mengulang penambahan durable baru, gunakan project Compose terpisah
dengan volume baru. Muat credential dari `.env`, siapkan direktori `/tmp/report-session` dan
`.secrets/report-session`, lalu lakukan login seperti pada bab P3 untuk memperoleh
access token. Ikuti urutan langkah dalam setiap bab; command outage, schema, dan
container mengubah kondisi stack.

Validasi record yang disertakan, dari root repo:

```sh
go run -mod=readonly docs/report/evidence/validate-records.go
```

`evidence/assess-load.py` menghasilkan tabel dan grafik dari berkas load/TCP yang
tersimpan (memerlukan matplotlib). Formulir AI dapat ditambahkan sebagai
`appendices/form-penggunaan-ai.pdf`, lalu build ulang.
