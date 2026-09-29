# Struktur lama

Jangan menambah entrypoint di root cmd. Baseline v1 menggunakan services/<name>/cmd/server agar entrypoint dapat mengimpor internal privat service tersebut. Lihat docs/contract.md D02.
