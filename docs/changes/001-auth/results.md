# 001 results

Checked on the VPS on 2026-11-24 with Go 1.27.4.

## Tests

`go vet ./...` is clean. `go test ./...` passes, and so does `go test -race ./...`, run in a `golang:1.27` container because the host has no C compiler.

| Area | Cases |
| --- | --- |
| `auth` | happy path and logout; explicit allowlist; disallowed and malformed emails rejected with no mail sent; a disallowed email can't redeem; a spent code is rejected on reuse; an expired code is rejected; a new code replaces the old one; 3 wrong guesses burn the code; codes stored as a 32-byte MAC; per-email and per-IP request limits, including for disallowed emails, then reset after the window; per-IP verify limit; a failed send drops the code; session expiry; turn cap; email body has a code and no links |
| `server` | unauthenticated `GET /api/me`, `POST /api/chat`, and `GET /api/starters` return 401 with no protected body; `/` and unknown pages redirect to `/login`; a forged cookie is rejected; public routes respond; full HTTP login flow; cookie flags `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`; logout ends the session; JSON error codes for 403, 400, 401, and 415; 429 with `Retry-After`; `X-Robots-Tag` and CSP on pages, APIs, redirects, and errors; logs contain neither the code nor the session token; client IP with and without `TRUST_FORWARDED_FOR` |
| `mail` | full exchange against a fake SMTP server over STARTTLS with a self-signed CA: AUTH PLAIN credentials, From/To/Subject/Content-Type headers, CRLF body, no links; refuses a server without STARTTLS and sends no credentials; dial errors leak neither the password nor the body |

## Live smoke run (real binary, SMTP pointed at a closed local port)

| Request | Response |
| --- | --- |
| `GET /` without a cookie | `303` to `/login` |
| `GET /api/me` without a cookie | `401 {"error":"unauthenticated"}` |
| `GET /login` | `200` with `X-Robots-Tag: noindex, nofollow` and the CSP |
| `POST /api/auth/request` with a disallowed email | `403 email_not_allowed`; log line `code_rejected_not_allowed` with the IP only |
| `POST /api/auth/request` with an allowed email | `502 email_send_failed`; log line contains only the dial error |
| A grep for the dummy secrets in the log | 0 matches |

## Not yet verified

- A live Gmail SMTP send to an external inbox. This needs the App Password in `.env` on the VPS and is on the M4 checklist.
