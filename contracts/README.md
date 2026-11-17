# Kontrak data

JSON Schema untuk data yang dipertukarkan antarservice:

| Schema | Isi |
|---|---|
| [hazard-event.schema.json](hazard-event.schema.json) | `HazardEvent` lengkap; field tambahan sumber disimpan dalam `attributes` |
| [hazard-summary.schema.json](hazard-summary.schema.json) | Tujuh field ringkasan yang boleh diterima Media |
| [hazard-envelope.schema.json](hazard-envelope.schema.json) | Event NATS yang memuat `HazardEvent`, revision, dan correlation ID |
| [field-report.schema.json](field-report.schema.json) | Body permintaan `POST /field-reports` yang akan ditambahkan pada M2 |

[examples/](examples/) berisi contoh data sintetis. Tes juga memeriksa format UUID, timestamp, dan kesesuaian `message_id` dengan ID hazard serta revision.

M2 menambahkan `SATELIT/WILDFIRE` dan `SENSOR_PANTAI/COASTAL`. Tipe Go bersama ada di [hazard.go](hazard.go); interface adapter dan keluaran processor ada di [ingestion.go](ingestion.go). Contoh adapter dapat dijalankan dengan `go test ./contracts -run Example -v`. Perubahan ini menyiapkan kontrak; kedua sumber baru belum terhubung ke aplikasi.

Jalankan dari root repo:

```sh
go test ./contracts
```
