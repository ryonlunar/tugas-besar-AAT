# Demo console

Run from the repository root on Linux or WSL, with Docker Compose, Go, Python 3, OpenSSL and `ss` installed:

```sh
go run ./tools/demo-console
```

Open http://localhost:8099. Configure `.env` with local secrets and an explicit `COMPOSE_PROJECT_NAME` first. On Overview, prepare JWT keys, then build and start the stack.

- **P1–P5:** numbered demo steps, specification text, controls and full trials.
- **Inspect:** API records, schemas, storage, outbox, consumers and masked configuration.
- **Evidence:** results, revision, execution logs and downloads.
- **Console:** execution output or selected service logs; one action at a time.

Runs continue when the browser closes. Cancel waits for trial cleanup; manual changes remain until the corresponding restore/start control is used. A forced process kill cannot guarantee cleanup. Check service state and restore source defaults before retrying an unfinished run.

Private run files are stored in `docs/evidence/private/demo-console/`. Existing repository evidence is labeled historical.

Optional flags: `--repo`, `--env-file`, `--evidence-dir`, `--spec-dir`, `--listen`. PDF links expect the supplied course documents in the repository's parent directory, or in `--spec-dir`. The listener accepts only loopback addresses.
