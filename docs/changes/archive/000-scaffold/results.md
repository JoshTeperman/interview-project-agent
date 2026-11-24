# 000 results

Checked on the VPS on 2026-11-24 with Go 1.27.4.

| Check | Result |
| --- | --- |
| `go vet ./...` | clean |
| `go test ./...` | pass (config: defaults, Anthropic default model, 10 fail-closed cases, secret masking, email allowlist matching) |
| Static binary build (`CGO_ENABLED=0`) | builds, 9.9 MB |
| Empty env | exits 2 and lists all 5 problems: `SMTP_USER`, `SMTP_PASS`, `SNAPSHOT_DIR`, allowlist, `OPENAI_API_KEY` |
| Dummy env with no API key | exits 2: `OPENAI_API_KEY is required when LLM_PROVIDER=openai` |
| Valid dummy env | starts and `/healthz` returns `ok`; the startup log shows `api_key=<set> smtp_pass=<set>`. A grep for the dummy secret values in the log returns 0 matches. |
| Secrets in tree or history | none: `.env` is ignored and `.env.example` has empty values |
| Image build from a clean export of the commit (`git archive`) | builds; 15.8 MB; image user `nonroot:nonroot` (Docker 29.1.3) |
| `docker compose run` with an empty `.env` | refuses to start and lists the 4 missing values (Compose supplies `SNAPSHOT_DIR`) |
| `docker compose up` with a dummy `.env` | `/healthz` returns `ok`; `ReadonlyRootfs=true`, memory 268435456 bytes (256 MB), user `65532:65532`, `CapDrop=[ALL]`, `/snapshot` mounted read-only, `/data` writable; 0 dummy secrets in container logs |
| Hostname, tailnet IP, or email in any commit | none (`git grep` over both commits) |
