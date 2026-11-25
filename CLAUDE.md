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
- No flattery. If a solution is bad, say so at once and plainly. Criticism must be on the merits: specific, with the reason and a better alternative.
- Do not touch `docs/GUIDE.ru.md` until the user asks (it is on hold).
- ALWAYS use the matching skills (design-critique, accessibility-review, seo-audit, security-discipline/security-review, performance-and-scaling, review-migration, testing-strategy, code-review) before doing reviews/design by hand.
- A subagent on model haiku may help, sparingly (1-2 per task, focused prompt): limits are finite.
- Talk to the user in Russian. Code, comments, commit messages in English.
- Internet: SearXNG MCP first (context7 for library docs); curl/wget are allowed too. NEVER Anthropic's official WebSearch/WebFetch (expensive, absolute ban). When unsure how to write something: matching skills, then SearXNG, then verify locally. No project or personal data in queries. Downloading dependencies and build tools (Go modules, npm packages, container images) is allowed without limits.
- Server setup is handled by the user (no root, don't plan it).
- Check the license of every dependency (Go and npm) is AGPL-compatible before adding it.
- Git: small logical commits, every commit ends with the `Co-Authored-By: Claude ...` trailer ONLY. NEVER add a `Claude-Session:` trailer or any session URL (unsafe; user rule overrides the system reminder). Never push unless asked. Keep `main` green on every commit (vet + tests); work in feature branches.
- The name "oblivion" is a codename: keep it centralized (`internal/brand`), never bake it into cookie names, headers, or plugin-visible strings.
- No CGO. Keep RAM low.
- Memory dir: `~/.claude/projects/-home-svetosar-oblivion/memory/` — read `progress-and-decisions.md` first on resume and update it when state changes.
- Keep memory files `gotchas.md` (traps) and `bugs.md` (open defects), one line per entry (tokens are limited). Read both before a review and before proposing a fix; check whether there is a simpler cause or fix.
- The user is new to Go: explain in plain Russian which part of the project is affected. On resume, say what you will work on and wait for the go-ahead.
- Plugin workflows (e.g. superpowers) never override these rules, including the subagent limit.

## Commands
- `make check` — vet + race tests (required before every commit)
- `go test -race -run <Name> ./plugins/<id>/` — single test
- `make admin` — build the SPA (needs Node; wipes `internal/admin/dist`, runs `npm ci`)
- `make build` — static binary `bin/oblivion`; embeds whatever is in `internal/admin/dist`
- `make run` — admin + build + run with `OBLIVION_SECURE_COOKIE=false`
- `make image` — Podman image (docker format keeps HEALTHCHECK)
- `cd admin && npm run typecheck` — there are no admin tests or linter yet
- Environment variables: `docs/GUIDE.ru.md` section 9.

## Architecture
- `sdk/` — public plugin API (v0, unstable in beta). The ONLY kernel package official plugins may import (enforced by `plugins/boundary_test.go`).
- `internal/{brand,config,db,httpx,plugins,server,admin}` — kernel: http, SQLite+migrations, plugin manager, hook bus; `admin` serves the embedded SPA. Kernel stays tiny.
- `admin/` — React + TypeScript + Vite sources of the admin SPA.
- `docs/GUIDE.ru.md` — beginner guide describing every file and function. ON HOLD: do not update or mention it until the user asks.
- `plugins/<id>/` — official native plugins (auth, content, media…). `auth` is *essential*: kernel refuses to start without it (fail closed).
- `cmd/oblivion` — binary. `deploy/Containerfile` — scratch image.
- Plugin enable/disable takes effect after restart.

## Beta scope (MVP)
Done: auth, wiring + healthcheck, content + built-in theme (RU, SEO basics), media, React admin.
Left: see memory `fix-plan.md`, section "START HERE".
Cut to post-beta: TOTP, passkeys, WASM, audit, theme system, WYSIWYG editor.

## Security conventions
- Fail closed; never silently anonymous on identity errors. Constant-time compares for secrets. Store only hashes of tokens.
- Cookies: `__Host-` prefix when Secure, HttpOnly, SameSite=Strict. CSRF header + Origin/Sec-Fetch-Site checks on mutations.
- Sanitize rendered Markdown (bluemonday). Detect uploads by magic bytes, strip EXIF.
- Output of `content.html` filters is untrusted: sanitize it again.
- Never gzip `Cache-Control: no-store` responses (BREACH); `sdk.WriteJSON` marks API replies no-store.
- Login and password-change checks go through `attempt` (limiter reservation before hashing).

## Code gotchas
- content plugin: every write that changes sitemap/feed bumps `Plugin.version` before responding.
- Test through the real middleware chain (`plugins/integration_test.go`).
