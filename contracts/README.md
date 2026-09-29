# Wire contract M1

Schema JSON dan contoh sintetis untuk tes lintas-service, bukan shared business logic. Acuan: [contract](../docs/contract.md), [keputusan](../docs/decisions.md). Contoh ini bukan output implementasi.

- HazardEvent memakai nama/enum wajib PDF; extra source masuk attributes.
- Summary hanya tujuh field Ringkasan; additionalProperties=false mencegah raw-field leak.
- Envelope memakai full HazardEvent di data; revision dan correlation_id di luar entity.
- Tes Go mengaktifkan pemeriksaan format date-time dan UUID.
- Schema ini tidak membuktikan mapping/severity, dedup, otorisasi, delivery, atau performa; acceptance test tetap wajib.
- Schema menegakkan UUID, bentuk `message_id`, relasi `source`/`hazard_type`, dan `event_type`/`revision`. Equality lintas-field antara UUID/suffix `message_id`, `data.hazard_id`, dan `revision` tidak dapat dinyatakan oleh JSON Schema portabel; tes Go memeriksanya sebagai aturan tambahan.
- Validasi reproducible (termasuk `format` date-time/uuid dan kasus negatif): `go test ./contracts`.
