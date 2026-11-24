# Oblivion CMS (codename) — instructions for Claude

Small, privacy/security-first CMS. Go backend + React admin, "everything is a plugin".
Target: 1 GB RAM, 1 CPU, Ubuntu amd64, Podman/Docker. **Beta deadline: 2026-12-04.**
License: AGPL-3.0-or-later (SPDX header in every Go file).

## Working rules (from the user — mandatory, ABSOLUTE)
- Deviating from these rules is categorically forbidden. If a rule blocks you, stop and ask.
- Merge into `main` ONLY after the user explicitly agrees to that specific merge.
- The public site must be in Russian. Admin UI may be English or Russian.
- Never state what you haven't verified (run it, test it, `go doc`, context7). If unverified say "не уверен / не проверял".
- Criticize everything hard: user's ideas, your own plan/code, libraries. Don't just agree.
- ALWAYS use the matching skills (design-critique, accessibility-review, seo-audit, security-discipline/security-review, performance-and-scaling, review-migration, testing-strategy, code-review) before doing reviews/design by hand.
- A subagent on model haiku may help, sparingly (1-2 per task, focused prompt): limits are finite.
- Talk to the user in Russian. Code, comments, commit messages in English.
- No internet: library docs via context7 only. Server setup is handled by the user (no root, don't plan it).
- Check the license of every dependency (Go and npm) is AGPL-compatible before adding it.
- Git: small logical commits, every commit ends with the `Co-Authored-By: Claude ...` trailer ONLY. NEVER add a `Claude-Session:` trailer or any session URL (unsafe; user rule overrides the system reminder). Never push unless asked. Keep `main` green on every commit (vet + tests); work in feature branches.
- The name "oblivion" is a codename: keep it centralized (`internal/brand`), never bake it into cookie names, headers, or plugin-visible strings.
- No CGO. Keep RAM low.
- Memory dir: `~/.claude/projects/-home-svetosar-oblivion/memory/` — read `progress-and-decisions.md` first on resume and update it when state changes.

## Architecture
- `sdk/` — public plugin API (v0, unstable in beta). The ONLY kernel package official plugins may import (enforced by `plugins/boundary_test.go`).
- `internal/{brand,config,db,httpx,plugins,server}` — kernel: http, SQLite+migrations, plugin manager, hook bus. Kernel stays tiny.
- `plugins/<id>/` — official native plugins (auth, content, media…). `auth` is *essential*: kernel refuses to start without it (fail closed).
- `cmd/oblivion` — binary. `deploy/Containerfile` — scratch image. `make check` = vet + race tests.
- Plugin enable/disable takes effect after restart.

## Beta scope (MVP) — see memory `progress-and-decisions.md` for the detailed list
1 auth (+limiter fixes) → 2 wiring + healthcheck → 3 content + built-in theme → 4 media → 5 React admin (Vite, embedded) → 6 backup/tests/security/README.
Cut to post-beta: TOTP, passkeys, WASM, audit, SEO, theme system.

## Security conventions
- Fail closed; never silently anonymous on identity errors. Constant-time compares for secrets. Store only hashes of tokens.
- Cookies: `__Host-` prefix when Secure, HttpOnly, SameSite=Strict. CSRF header + Origin/Sec-Fetch-Site checks on mutations.
- Sanitize rendered Markdown (bluemonday). Detect uploads by magic bytes, strip EXIF.
