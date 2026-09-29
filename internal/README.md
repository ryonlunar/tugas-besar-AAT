# Batas business logic

Jangan membuat shared mapper/domain/auth policy di direktori ini. Baseline v1 menggunakan services/<name>/internal. Komunikasi antarservice harus melalui HTTP/NATS, bukan import fungsi. Schema JSON bersama ada di contracts/, bukan business-logic library.
