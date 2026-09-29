# Script pengujian

Script smoke yang tersedia:

- `bash scripts/smoke-source-mocks.sh` membangun dan menjalankan dua mock pada port lokal sementara; memeriksa seed BMKG, penolakan tanpa credential, toggle schema PVMBG v1/v2 dengan ID stabil, dan outage 503. Script memakai fixture/credential sintetis dan membersihkan prosesnya saat keluar.

Script ini tidak menguji Aggregator, Canonical Store, JetStream, Auth/API, consumer, atau acceptance P1–P5 secara penuh. Hasil yang benar-benar dijalankan dan batasnya dicatat di [verification.md](../docs/verification.md); jangan menyebut smoke mock sebagai E2E seluruh aplikasi.
