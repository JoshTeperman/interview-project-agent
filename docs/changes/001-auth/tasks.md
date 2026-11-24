# 001 tasks

- [x] `internal/ratelimit`: fixed-window limiter with pruning
- [x] `internal/mail`: SMTP sender over STARTTLS with `net/smtp`; plain-text minimal message; refuses servers without STARTTLS
- [x] `internal/auth`: 6-digit codes (crypto/rand), HMAC-stored, short-lived, single-use, burned after N wrong guesses; allowlist check before sending; per-IP and per-email request limits; per-IP verify limit; sessions with TTL and a turn counter
- [x] `internal/server`: default-deny routing, `requireAuth`, JSON auth endpoints, `__Host-session` cookie (HttpOnly, Secure, SameSite=Strict), security headers including `X-Robots-Tag`
- [x] `web/`: minimal login and signed-in pages, embedded
- [x] Tests: allowlist rejection, spent-code reuse, expiry, rate limits, unauthenticated access to chat routes, cookie flags, headers, no secrets in logs
- [x] Specs: `docs/specs/auth.md`; config spec updated
- [ ] Live Gmail SMTP send to an external inbox: needs the App Password in `.env` on the VPS (M4 checklist item)
