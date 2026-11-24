# interview-project-agent

A private, read-only web chat that lets interviewers ask about a project's code and the approach behind it. Read `docs/scoping.md` before proposing anything: it holds the problem, requirements (R#, U#), milestones and the decision index.

## Rules

1. `docs/scoping.md` is the source of truth. Tick requirements and update milestone status when a change lands.
2. Each milestone is a change in `docs/changes/NNN-name/` with `proposal.md` (what, why, and that change's decisions as `NNN-D#`), `tasks.md`, and `results.md` (measurements, real outputs; never invented numbers). Index every new decision in scoping Section 11. Decisions made without a human are marked `Claude (for review)`.
3. When a change is complete, update `docs/specs/` to describe built behaviour, then move the change folder to `docs/changes/archive/`.
4. Stop for human review between milestones.
5. Never commit secrets (`.env`, API keys, SMTP passwords). Never log tokens, magic codes, or API keys; use `config.Mask`.
6. The model gets only `list_files`, `search_files`, and `read_file`, jailed to the snapshot. Never add shell, write, or network tools.
7. This repo is public. `docs/scoping.md` and `docs/decisions-log.md` are local-only (gitignored) because they name private hosts, addresses, and mailboxes. Never commit hostnames, tailnet names or IPs, or email addresses: read them from env (`.env` on the VPS) or look them up at runtime (for example `tailscale status --json`), including in commit messages and PR bodies.

## Layout

- `cmd/server`: the web app entrypoint.
- `internal/`: config, HTTP server, auth, snapshot tools, LLM providers. Unit tests sit next to each package (`*_test.go`).
- `web/`: embedded HTML, CSS, and vanilla JS (from M3).
- `evaluation/`: fixture snapshots and agent-loop checks (from M2).
- `deploy/`: VPS deployment files (from M4).

## Commands

- `go test ./...`: run all tests.
- `go vet ./...`: static checks.
- `CGO_ENABLED=0 go build -o bin/server ./cmd/server`: build the static binary.
