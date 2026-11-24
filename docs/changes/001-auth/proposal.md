# 001: Email magic-code auth via configurable SMTP (M1)

## What

A login page and JSON endpoints for requesting and redeeming a 6-digit emailed code, gated to the configured domains and explicit allowlist. Includes server-side sessions in an HTTP-only secure cookie, rate limits on code requests and verification, an SMTP mailer that requires STARTTLS, a default-deny auth gate on every non-public route, and security headers on every response.

## Why

No project data or chat UI may load before an allowed interviewer proves they own their email (R1, R2, R14). Codes and tokens must never be stored in plain text or logged (R3). Limits bound abuse if the URL leaks (R10). Search engines must not index the app (R12).

Requirements: R1, R2, R3, R10 (auth limits), R12, R14, U1, U2 (auth error states), S-D4, S-D10.

## Decisions

| ID | Decision | Rationale | Decided by |
| --- | --- | --- | --- |
| 001-D1 | Codes are stored as HMAC-SHA256 under a random per-process key. Session tokens are 256-bit random values, stored as SHA-256. Both live in memory only, so a restart voids pending codes and signs everyone out. | Meets "stored hashed" without a database. A 6-digit code is brute-forceable offline from a plain hash; a keyed MAC is not. Losing sessions on restart is acceptable for an on-demand interview tool. | Claude (for review) |
| 001-D2 | A disallowed email gets an explicit `403 email_not_allowed`. Every request, allowed or not, counts against the per-IP limit. | U2 asks for rejected emails to be shown clearly. That reveals whether an address is allowed, so the per-IP limit bounds probing. | Claude (for review) |
| 001-D3 | Routing is default-deny. Only `/login`, `/healthz`, `/robots.txt`, `/static/*`, and `/api/auth/*` are public; everything else goes through `requireAuth` via `Server.Protected`. Unauthenticated API calls get `401`, and page requests are redirected to `/login`. | New routes in M2 and M3 are protected unless someone deliberately makes them public. | Claude (for review) |
| 001-D4 | CSRF defence: a `__Host-session` cookie with `SameSite=Strict`, plus JSON-only POST bodies (other content types get 415). | Cross-site forms can't send JSON without a CORS preflight, and the app serves no CORS headers. No token plumbing needed. | Claude (for review) |
| 001-D5 | The client IP comes from `RemoteAddr` by default. `TRUST_FORWARDED_FOR=true` switches to the last `X-Forwarded-For` entry. M4 sets it once the Funnel proxy's behaviour is confirmed. | Behind a sidecar proxy, every request shares the proxy's address, which would make the per-IP limits global. Trusting the header must be opt-in. | Claude (for review) |
| 001-D6 | The mailer refuses to send if the server doesn't offer STARTTLS, and never puts the password or message body in errors. The From address is `SMTP_USER`, with `SMTP_FROM_NAME` as the display name. | Credentials never cross the network in clear text. Gmail requires From to match the authenticated account. | Claude (for review) |
| 001-D7 | Every response also carries a strict CSP (`default-src 'self'`), `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and `Cache-Control: no-store`. `/robots.txt` disallows everything. | Cheap hardening for a page that shows confidential material. `no-store` keeps answers out of shared caches. | Claude (for review) |
| 001-D8 | M1 ships minimal login and signed-in pages in `web/` (embedded with `//go:embed`). M3 replaces them with the full chat UI and themes. | The login flow needs a page to be usable and testable now. Putting it in `web/` already follows S-D12. | Claude (for review) |
| 001-D9 | New settings: `VERIFY_PER_IP_HOUR` (default 30) and `TRUST_FORWARDED_FOR` (default false). | Verification needs a per-IP limit as well as the per-code attempt limit, so a caller can't burn through codes for many addresses. | Claude (for review) |
| 001-D10 | Operational logs record the email address on `code_sent` and `signed_in`, never the code or token. Rejected attempts log only the IP. | Josh wants to know who used the tool ahead of a debrief. Emails aren't secrets. | Claude (for review) |
