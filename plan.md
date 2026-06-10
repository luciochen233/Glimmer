# Hardening / Robustness Plan

> Progress: all five items done. Item 5 verified in a live browser session
> (every page exercised, zero console errors / CSP violations).

Follow-ups from the 2026-06-09 security review (fixes from that review already
landed in `962f5d9`). Status legend: ☐ todo · ◐ in progress · ☑ done

## 1. ☑ Image decompression-bomb guard

`generateThumbnail` and `handleAdminUploadResize` call `image.Decode` with no
dimension check. A valid small PNG can declare huge pixel dimensions and OOM
the RPi Zero (512 MB) on decode. `GET /uploads/thumb/{filename}` is public and
triggers generation on first request.

- Add a bounded decode helper: `image.DecodeConfig` first (header only),
  reject anything over 24 MP, use `int64` math so the width×height product
  cannot overflow 32-bit `int` on ARMv5.
- Use it in both `generateThumbnail` and the resize handler.

## 2. ☑ Graceful shutdown

`Start()` calls `ListenAndServe` directly; SIGTERM/Ctrl-C kills in-flight
requests and gives SQLite no chance to close cleanly.

- `signal.NotifyContext` for SIGINT/SIGTERM, then `srv.Shutdown` with a 10 s
  timeout.

## 3. ☑ `Secure` flag on the CSRF cookie

The session cookie sets `Secure` via `isHTTPS()`; the `csrf` cookie never
does. Make `csrfToken` a `*Server` method and set `Secure: s.isHTTPS(r)`.
(Stays non-HttpOnly on purpose — `upload.js` reads it.)

## 4. ☑ Handler test coverage

`api_test.go` / `mcp_test.go` are thorough but the web handlers (auth, CSRF,
upload serving) have zero tests. Add `handlers_test.go` covering at least:

- unauthenticated `/admin/*` requests redirect to login
- state-changing POSTs without a CSRF token are rejected (incl. the resize
  route fixed in `962f5d9`)
- `/uploads/{filename}`: invalid/traversal names 404; non-image files get
  `Content-Disposition: attachment` + octet-stream
- login: wrong password → 401, correct → session cookie + redirect
- oversized-image rejection from item 1

## 5. ☑ Drop `'unsafe-inline'` from `script-src` (CSP)

Done: all inline `<script>` blocks and `on*=` attributes moved to per-page
files under `static/js/` (`base.js` carries shared chrome: QR modal,
`glimmerCopy`, `form[data-confirm]`). Template data is passed via `data-*`
attributes instead of `{{...}}` interpolation in JS. `script-src` is now
`'self'`; `style-src` keeps `'unsafe-inline'` for the `style=""` attributes.
Verified live: shorten flow, login, QR modal, paste editor (token radios,
name preview), paste cards (preview dialog, expand/close), public paste view,
token prompt redirect, uploads page — no console errors or CSP violations.

---

**Verification for each item:** `go build ./...`, `go vet ./...`,
`go test ./...` must pass; CLAUDE.md conventions apply (no new deps,
RPi-Zero-friendly memory use).
