# Client API

Pemilik: Orang 2. Client API memverifikasi JWT RS256 secara lokal, lalu membaca melalui HTTP internal Aggregator. Service ini tidak memiliki PostgreSQL credential dan tidak boleh berada di `store-net`.

Set `JWT_PUBLIC_KEY_FILE`, `AGGREGATOR_BASE_URL`, dan `AGGREGATOR_API_KEY`. Endpoint publik: `GET /v1/hazards`, `GET /v1/hazards/{hazard_id}`, `GET /health`, dan `GET /ready`.

Media hanya menerima projection tujuh field summary. Upaya `view=full` atau meminta field raw menerima 403 dari server. `field-team` dan `bnpb-internal` dapat memakai `view=full` setelah JWT tervalidasi.

Verifikasi: `go test ./services/client-api/...`.
