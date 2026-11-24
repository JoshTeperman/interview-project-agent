# 000: Go module scaffold, config, and container skeleton (M0)

## What

The Go module, the docs tree, fail-closed configuration loading, a minimal server entrypoint (`/healthz`), a non-root Dockerfile, a Compose file with a read-only snapshot mount and a memory cap, and `.env.example`.

## Why

Every later milestone needs a place to live and a single, validated config. Failing closed at startup means a deploy with a missing API key, SMTP password, allowlist, or snapshot never serves traffic.

Requirements: R3 (no secrets committed or logged), R7 (read-only local snapshot mount), R9 (container skeleton that M4 locks down), Section 9, S-D8.

## Decisions

| ID | Decision | Rationale | Decided by |
| --- | --- | --- | --- |
| 000-D1 | `docs/decisions-log.md` holds the log's rules and points to scoping Section 11, which keeps the one decision table. | Two copies of the same table drift. Section 11 was already a superset. | Claude (for review) |
| 000-D2 | Unit tests sit next to their package as `*_test.go`, the Go convention. A top-level `tests/` is created only if cross-package integration tests need it. | `go test ./...` finds them without extra wiring, and tests can reach unexported helpers. | Claude (for review) |
| 000-D3 | Change folders are numbered from `000`, so `000` to `004` map to M0 to M4. | Easier to read than an off-by-one mapping. | Claude (for review) |
| 000-D4 | The runtime image is `gcr.io/distroless/static-debian12:nonroot` (uid 65532, CA certs included). Compose sets `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, `mem_limit: 256m` (no swap), and `pids_limit: 128`. | A static Go binary needs no shell or package manager. 256 MB is ample for the app and leaves room on a box with about 1.9 GB available. | Claude (for review) |
| 000-D5 | Default limits: code TTL 10m, session TTL 12h, 10 code requests per IP per hour, 3 per email per hour, 5 verify attempts per code, 6 chats per session per minute, 12 per IP per minute, 2000-char messages, 40 turns per session, 6 tool steps per turn. All can be overridden through env. | Scoping set only the step cap (6). The rest are conservative starting values for a handful of interviewers. | Claude (for review) |
| 000-D6 | The question log goes to a writable `DATA_DIR` (`/data` in Compose), separate from the read-only snapshot. | The snapshot mount is read-only, and the root filesystem is too. | Claude (for review) |
| 000-D7 | The repo is public. The imported source docs (`docs/scoping.md`, `docs/decisions-log.md`) are gitignored and were removed from history before the first push. Hostnames, IPs, and mailbox addresses come from env or runtime lookup, never from committed files. | Josh approved a public repo on these terms (24 Nov 2026). The source docs name the tailnet host, its IP, and the sender mailbox. | Josh |
| 000-D8 | Docker (`docker.io` 29.1.3, `docker-compose-v2` 2.40.3) is installed on the VPS from Ubuntu's own packages. | Josh approved installing Docker. Distro packages avoid piping a remote install script into a root shell. The only change to existing firewall rules is Docker's standard FORWARD policy of DROP; Tailscale's forward chain still accepts its own traffic. | Josh (install), Claude (package source) |
