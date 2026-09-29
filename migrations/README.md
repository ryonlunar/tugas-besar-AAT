# Migration Canonical Store

Pemilik: Aggregator/Hazim. Migration Canonical Store berada di `services/aggregator/internal/store/migrations/` dan di-embed ke binary Aggregator. Saat startup, Aggregator memvalidasi checksum ledger, mengambil PostgreSQL advisory lock, lalu menerapkan migration berurutan. Client API dan consumer tidak menjalankan atau mengakses migration Canonical Store; SQLite Auth/consumer dimigrasikan secara privat oleh service pemiliknya saat implementasinya tersedia.
