# AGENTS.md

Chinese reference copy: [`AGENTS.zh-CN.md`](./AGENTS.zh-CN.md).

## Scope

This file applies to the repository root and all of its subdirectories. There is currently no deeper `AGENTS.md`; if one is added later, the most specific file for a target path takes precedence.

This is a self-hosted, single-site personal blog: a Go 1.26 modular monolith with SQLite WAL, Markdown as the canonical body format, server-side rendering, and optional capabilities disabled by default. Public pages, the admin area, background work, and the CLI share one application. Production does not require Node.js, Redis, a separate search service, or a message queue.

## Before starting work

1. Run `git status --short` first. Preserve the user's existing changes, untracked files, and runtime artifacts; never use `git reset --hard`, `git checkout --`, or cleanup commands to overwrite them.
2. Read the design documents relevant to the task. Usually start with [`CONTEXT.md`](./CONTEXT.md), [`docs/architecture.md`](./docs/architecture.md), [`docs/data-model.md`](./docs/data-model.md), [`docs/extensions.md`](./docs/extensions.md), and [`docs/adr/README.md`](./docs/adr/README.md). For theme, plugin, import, recovery, or performance work, also read the relevant files under `docs/development/`, `docs/security/`, `docs/progress/`, and the applicable ADRs.
3. Use the current code and tests to establish behavior that is already implemented; design documents may also describe future goals. If documentation and code disagree, do not silently expand scope: call out the conflict and update the documentation or add an ADR when needed.
4. Find existing services, queries, templates, and tests before adding an abstraction. Prefer small, focused, reversible changes.
5. Do not commit, push, publish images, or change remote resources unless the user explicitly asks.

## Product and architecture invariants

- One deployment has exactly one `Site` and one `Owner`. Do not introduce generic users, roles, multi-tenancy, reader accounts, or memberships.
- Articles and pages are the only content types. Markdown in the database is the authoritative body; caches, search indexes, analytics, and media variants must be rebuildable from authoritative data.
- A content item's public identity is its stable permalink/slug, not an internal SQLite row ID. A published slug change must preserve the old path and create a redirect.
- `content_revisions` are immutable. An editing snapshot is not a formal revision. Public reads must never use editing snapshots or drafts.
- Public pages use only published revisions. Any write that can change the public rendering must increment `system_state.render_epoch` so old page-cache entries become invalid.
- Each business write uses one short SQLite write transaction. Do not do Markdown-heavy work, image processing, network requests, email, or Webhook delivery inside the transaction; run external side effects after commit through durable jobs.
- Themes are restricted Go `html/template` templates plus static assets and cannot execute server-side code. Plugins are trusted Go code compiled into the binary; they cannot upload or execute arbitrary scripts or access a raw `*sql.DB`, a general filesystem, or a process executor.
- Plugins are disabled by default, and disabling one preserves its configuration and data. Plugin routes must be guarded by the host's enabled check and use the plugin's namespace; events and tasks must be versioned and idempotent.
- The site must not request third-party fonts, analytics scripts, icons, or CDN assets by default. New external network access needs an explicit adapter, timeouts, failure semantics, and security review.
- Resource budgets are constraints, not suggestions: the application targets about 0.85 CPU/256 MiB in its container, with a default 192 MiB Go heap soft limit. Caches, connection pools, queues, and concurrency must be bounded.

## Code map and dependency direction

| Path | Responsibility |
| --- | --- |
| `cmd/blog/main.go` | `serve`, `healthcheck`, auth recovery, backup/restore, themes, archives, storage migration, import, status, and audit CLI |
| `internal/app` | Application wiring, route registration, lifecycle loop, and optional-plugin initialization |
| `internal/platform` | Configuration, SQLite, logging, secrets, IDs, slugs, pagination, and shared HTTP utilities |
| `internal/identity` | The single owner, passwords, TOTP, recovery codes, sessions, CSRF, and authorization |
| `internal/publishing` | Articles/pages, editing snapshots, immutable revisions, publishing, scheduling, unpublishing, and trash |
| `internal/organization` | Categories, tags, navigation, permalinks, and redirects |
| `internal/media` | Media, references, variants, local/S3 storage, and migration |
| `internal/presentation` | Safe Markdown rendering, public view models, page cache, and theme loading/switching |
| `internal/discovery` | Search, RSS, Sitemap, robots, SEO, and public discovery data |
| `internal/operations` | Data locks, jobs, backups, restore, upgrades, health checks, and audit |
| `internal/extensions` | Plugin Host, registry, settings, events, and durable task execution |
| `internal/comments`, `analytics`, `contentapi`, `webhooks`, `notifications`, `importer`, `archive` | Implemented optional capabilities, notifications, offline import, and content archives |
| `web/admin` | Server-rendered admin templates and CSS/JS embedded through `embed.FS` |
| `themes/default` | Embedded default theme and safe fallback; its templates and CSS/JS are embedded too |
| `themes/*` (for example `themes/example` or a local theme package) | Theme package source/examples, not a runtime plugin directory |
| `db/migrations` | Numbered Goose SQLite migrations embedded through `embed.FS` |
| `tests/browser`, `tests/fixtures` | Three-browser Playwright regression tests and import fixtures |
| `scripts` | Performance gate, stage acceptance, release, SBOM, license audit, and browser regression scripts |

Keep dependencies moving in this direction:

```text
HTTP / CLI adapter -> application service -> repository port/adapter -> SQLite
                                   -> after-commit event / durable job
theme renderer     -> immutable public view model
official plugin    -> narrow extensions.Host capability
```

HTTP handlers must not write SQL directly, and modules must not query another module's private tables. Coordinate across modules through service interfaces, dedicated queries, or versioned events. Do not add project-wide horizontal `controllers/`, `services/`, or `repositories/` layers, and do not introduce a universal `AppContext` or global service locator.

The `plugins/` path in the design documents is conceptual. There is currently no top-level `plugins/` directory; official plugin implementations and registration live in the relevant `internal/*` packages and `internal/app/app.go`.

## SQLite, migrations, and data rules

- Always build and test with the two project build tags: `fts5 sqlite_omit_load_extension`. `go test ./...` is not the standard command for this repository.
- `internal/platform/database` maintains one write connection and a bounded read pool, and verifies WAL, foreign keys, and FTS5. Preserve the constraints around `PRAGMA journal_mode=WAL`, `foreign_keys=ON`, busy timeout, synchronous mode, and bounded cache size.
- Add schema changes as the next numbered migration, for example `db/migrations/00011_short_name.sql`, with Goose `-- +goose Up` and an auditable `-- +goose Down`. Never edit or reorder an already-applied migration.
- `database.Open` applies migrations automatically. `blog migrate` opens the database and prints the migration version; it is not a production rollback tool. New migrations must cover an empty database, an upgraded database, and failure/restart behavior.
- Persist time as UTC Unix milliseconds. Objects exposed publicly or across instances need a stable `public_id`. Never put an internal auto-increment ID in a public URL.
- Keep write transactions short and idempotent. Never copy only the active SQLite main file while ignoring `-wal`/`-shm`; use the existing consistent snapshot backup flow.
- Every new index needs evidence from a real query. Permanent cleanup, retries, scheduled publishing, and cache cleanup must be batched and bounded to avoid long locks and unbounded growth.

## Security rules

- Preserve the existing session, CSRF, Origin, rate-limit, and security-header policies for the admin area and public write endpoints. Every new POST/PUT/DELETE endpoint must cover authorization, CSRF, replay/idempotency, and error responses.
- Use separate allowlist sanitization policies for Markdown bodies and comments. Raw HTML must not become a script-injection path. Theme templates use `html/template` autoescaping; do not add a general `safeHTML` bypass.
- Never put passwords, session tokens, TOTP secrets, recovery codes, API keys, Webhook secrets, SMTP passwords, or complete visitor privacy data in logs, audit records, test output, error messages, or commits.
- Bound uploads, ZIPs, theme packages, and import files by file size, total size, file count, paths, and extraction behavior. Reject traversal, absolute paths, symlinks, and special files. Trust detected MIME and decoding results rather than extensions; SVG is rejected by default.
- Backups contain authentication secrets and are unencrypted by default with `0600` permissions. Never commit or publicly store them. Verify archives, acquire the data lock, and preserve a rollback copy before restore or dangerous deletion.
- Network adapters must set timeouts, reject unnecessary redirects and private-network traversal, and convert remote failures into retryable durable jobs. A remote outage must not roll back a successful content publication.

## Frontend, themes, and accessibility

- Prefer complete server-side rendering for the public site and admin area; JavaScript is progressive enhancement. Do not add a full SPA, global client-side store, or new frontend framework unless the requirement and an ADR explicitly approve it.
- After changing templates, CSS, or JS, preserve a usable no-JavaScript path, keyboard focus behavior, semantic landmarks, ARIA state, visible focus rings, sufficiently large touch targets, and no responsive horizontal overflow.
- Public pages, admin pages, login, setup, and previews share theme preference and consistent drawer/keyboard semantics. Consider desktop, tablet, mobile, narrow, and short viewports.
- The default theme is embedded through `themes/default/embed.go`. Templates must not read environment variables, files, database entities, or private plugin configuration; they consume safe, immutable public view models only.
- Theme installation and activation must keep the current working theme as a fallback and validate package limits, templates, API compatibility, and representative rendered samples. A broken package must never take over the site.
- Preserve the default-theme size, third-party-resource, LCP/INP/CLS, and public-response budgets documented by ADR-0029 and ADR-0031. UI changes should run the relevant Go rendering tests and browser regression suite.

## Common commands

Run Go commands from the repository root and use the fixed project tags:

| Command | Purpose |
| --- | --- |
| `make test` | Run all Go tests |
| `make test-race` | Run the race detector |
| `make vet` | Run `go vet` |
| `make build` | Build `bin/blog` |
| `make run` | Run locally with `config.example.toml` |
| `make perf-gate` | Run ADR-0031 performance tests and benchmarks |
| `make stage3-acceptance` | Run tests, race, vet, module verification, and health/home checks if Compose is already running |
| `make browser` | Run Chromium/Firefox/WebKit against default `BASE_URL=https://localhost`; requires the site and Playwright |
| `BROWSER_STRICT=1 make browser` | Fail when Playwright is unavailable; use for release acceptance |
| `make sbom` / `make license-audit` | Generate supply-chain reports under `dist/` |
| `make release` | Run release checks, build multi-architecture OCI output, generate SBOM/license reports, and checksums |
| `docker compose config` | Validate Compose configuration |
| `docker compose up --build -d` | Build and start local app + Caddy in the background |
| `docker compose down` | Stop local services without deleting named volumes |
| `go mod verify` | Verify dependency module integrity |

If Go is not installed locally, prefer the Docker fallback built into the project scripts. To run only the full test suite:

```bash
docker run --rm -v "$PWD:/workspace" -w /workspace golang:1.26.0-bookworm \
  sh -ec 'go test -tags "fts5 sqlite_omit_load_extension" ./...'
```

`make release` creates a local OCI archive by default. Set `PUSH=1` only when the user explicitly requests an image push. `dist/`, `bin/`, caches, reports, and runtime data are generated artifacts; do not hand-edit or commit them, and do not treat them as a substitute for source tests.

Browser regression defaults to `https://localhost` and ignores local HTTPS errors. Set `BASE_URL` for a temporary instance. Enable `ARTICLE_SMOKE=1` only when the isolated article fixture is prepared; otherwise that test is intentionally skipped.

## CLI and operations

Common subcommands include `blog healthcheck`, `blog status`, `blog audit list`, `blog backup create|verify|drill|list`, `blog upgrade prepare`, `blog restore`, `blog theme install|list|activate|rollback`, `blog archive export|verify|import`, `blog storage migrate`, and `blog import wordpress|ghost|markdown`.

A typical local startup is:

```bash
cp .env.example .env
docker compose up --build -d
curl --insecure https://localhost/livez
curl --insecure https://localhost/readyz
```

`BLOG_SITE_ADDRESS` is the public base used for canonical URLs, Open Graph, RSS, Sitemap, robots, and `llms.txt`; set it to the URL visitors actually use before deployment. Inject secrets through environment variables, Docker secrets, or permission-restricted files.

The default Compose deployment uses `app` and `caddy`, with application data under `/data/site` in a named volume. Ordinary `docker compose down` does not delete data. Stop the production app before `blog restore --replace`; after restore, check health and public output before handling the old-data rollback directory reported by the command.

## Verification by change type

- Go business/service changes: run a focused `go test -tags 'fts5 sqlite_omit_load_extension' ./internal/<package>`, then `gofmt`, `make test`, and `make vet`.
- Identity, authorization, uploads, theme packages, imports, backup/restore, migrations, or external-network changes: add failure-path, restart/idempotency, and isolation tests; at minimum run `make test-race`. For publishing or recovery work, also run `make stage3-acceptance`.
- Database changes: add migration and constraint tests, verify empty and upgraded databases, and confirm read/write connections, WAL, backup, and restore behavior.
- Public pages, admin templates, CSS/JS, or themes: preserve embed tests, run `make test` and `make browser`, and when appropriate run `BROWSER_STRICT=1 make browser` and `make perf-gate`.
- Release or dependency changes: run `go mod verify`, `make test-race`, `make vet`, `make perf-gate`, `make sbom`, and `make license-audit`. New dependencies must be version-pinned, license-compatible, evaluated for memory/failure impact, and wrapped behind a replaceable boundary.

## Testing and verification

For coding tasks, calibrate how much testing and verification a change requires. This can help avoid unnecessary tests or repeated checks for small changes.

Do not write tests for reversible, low-impact changes that mirror the implementation. If you do choose to verify your work with tests, make sure that the tests are meaningful and necessary to verify implementation.

Run tests appropriate to the change and complete required checks. Once those pass, broaden or repeat testing only when new changes, failures, or unresolved concerns justify it; otherwise, continue toward completing the task.

## Definition of done

Before handing off a change, confirm that:

- The change is limited to the user's request and necessary tests/documentation; unrelated user files were not cleaned or reset.
- Go files are `gofmt`ed, `git diff --check` passes, and no secrets, temporary databases, backups, or generated artifacts are included.
- Relevant tests and quality gates were run. If a check could not run because Go, Docker, Playwright, or an external service was unavailable, say so explicitly instead of claiming it passed.
- README, design documentation, or an ADR is updated when behavior, configuration, CLI, theme/plugin contracts, or operational procedures change.
- The handoff states what changed, what was verified, and any remaining risk or unrun checks.
