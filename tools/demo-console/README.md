# Demo console

From the repository root on Linux/WSL, with a configured `.env`:

```sh
go run ./tools/demo-console
```

Open http://localhost:8099. Requires Go, Bash, curl, Docker Compose, Python 3, OpenSSL and `ss`.

Buttons place the actual Docker, curl or Python commands in xterm.js. Edit the prompt or type a command yourself. Enter runs the exact text through Bash; Shift+Enter adds a line, Escape discards. The small info control beside each helper explains its default command in context. `.env` values are exported into the command environment. Temporary artifact paths are shown in the log. Process output streams into the terminal with credentials masked. Use Stop or Ctrl+C to cancel. Expand for screenshots; Runs saves the command, log and available JSON. Each run starts a new Bash process in the repository root. Closing the browser does not stop a running command.

Problem helpers perform separate checks. P3 saves each client session privately, then reuses it for reads, natural expiry and refresh. Run those steps in order; reads never log in or refresh automatically. JSON reads default to Pretty, adding a visible `| python3 -m json.tool`; Raw omits that pipe. The toolbar choice applies to the next queued helper. HTTP headers and status remain visible. Source reads select the last two minutes with an editable `since` query.

Service switches ask for confirmation before placing the command. Start/stop affects only that service. PVMBG controls change schema, delay and outage live. Environment inspection is read-only; restart the console after editing `.env`. Trials attempt cleanup; manual changes remain until restored. Forced termination can leave an unfinished run.

Private files: `docs/evidence/private/demo-console/`. Flags: `--repo`, `--env-file`, `--evidence-dir`, `--spec-dir`, `--listen`. The supplied PDFs belong in the repository's parent directory or `--spec-dir`. The listener accepts only loopback addresses.
