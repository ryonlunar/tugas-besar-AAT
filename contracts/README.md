# Kontrak data M1

JSON Schema untuk data yang dipertukarkan antarservice:

| Schema | Isi |
|---|---|
| [hazard-event.schema.json](hazard-event.schema.json) | `HazardEvent` lengkap; field tambahan sumber disimpan dalam `attributes` |
| [hazard-summary.schema.json](hazard-summary.schema.json) | Tujuh field ringkasan yang boleh diterima Media |
| [hazard-envelope.schema.json](hazard-envelope.schema.json) | Event NATS yang memuat `HazardEvent`, revision, dan correlation ID |

[examples/](examples/) berisi contoh data sintetis. Tes juga memeriksa format UUID, timestamp, dan kesesuaian `message_id` dengan ID hazard serta revision.

Jalankan dari root repo:

```sh
go test ./contracts
```
