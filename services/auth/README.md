# Auth Service

Pemilik: Orang 2. Service ini menerbitkan JWT RS256 60 detik dan melakukan rotating opaque refresh token. Hanya hash SHA-256 refresh token yang disimpan dalam SQLite privat.

Set `MEDIA_CLIENT_SECRET`, `FIELD_TEAM_CLIENT_SECRET`, `BNPB_INTERNAL_CLIENT_SECRET`, dan `JWT_PRIVATE_KEY_FILE`; jangan commit nilainya atau key private. Runtime membutuhkan volume writable pada `AUTH_STATE_PATH` (default `/data/auth.db`).

Endpoint: `POST /v1/auth/login`, `POST /v1/auth/refresh`, `GET /health`, dan `GET /ready`. Respons token memakai `Cache-Control: no-store`. Reuse refresh token mencabut seluruh family refresh; JWT access lama tidak dicabut instan dan tetap maksimal 60 detik.

Verifikasi: `go test ./services/auth/...`.
