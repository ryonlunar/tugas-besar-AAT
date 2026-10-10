# Auth Service

Menerbitkan JWT RS256 untuk Media, Field Team, dan BNPB internal. Access token berlaku 60 detik secara default; refresh token dirotasi setiap kali digunakan dan disimpan sebagai hash dalam SQLite.

## Konfigurasi

Memerlukan `MEDIA_CLIENT_SECRET`, `FIELD_TEAM_CLIENT_SECRET`, `BNPB_INTERNAL_CLIENT_SECRET`, dan `JWT_PRIVATE_KEY_FILE`. Lokasi SQLite diatur melalui `AUTH_STATE_PATH` (default `/data/auth.db`) dan harus dapat ditulis. Setup kunci dan service tersedia di [README utama](../../README.md#menjalankan-aplikasi).

## Endpoint

| Endpoint | Fungsi |
|---|---|
| `POST /v1/auth/login` | Menukar `client_id` dan `client_secret` dengan access token serta refresh token |
| `POST /v1/auth/refresh` | Menukar `refresh_token` dengan pasangan token baru |
| `GET /health` | Status proses |
| `GET /ready` | Kesiapan service |

Penggunaan ulang refresh token lama mencabut seluruh rangkaian refresh token terkait. Access token yang sudah terbit tetap berlaku hingga kedaluwarsa.

## Pengujian

```sh
go test ./services/auth/...
```
