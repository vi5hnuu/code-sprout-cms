# code-sprout-cms

Content API for Code Sprout: articles, blogs, practice problems, taxonomy, and reader
engagement (likes, bookmarks, comments). It is built to start at 1M+ items and keep growing
without a redesign.

Related services:
- **auth** (`IdeaProjects/auth`) issues access tokens. This service only verifies them, locally,
  against auth's JWKS. CMS roles (contributor, reviewer, editor, admin) live here, not in auth:
  a site owner (auth `ROLE_ADMIN`) is allowed everything and hands out roles with `/v1/admin/roles`.
- **ls-judge** (`IdeaProjects/ls-judge`) runs code from runnable snippets and problem submissions.
- **web** (`laxmi-solutions-nextjs/apps/code-sprout-cms`) renders public pages with SSG/ISR and
  hosts the authoring studio.

## Architecture in one screen

```
CDN ─► Next.js (ISR) ─► cmd/api ─► Postgres primary ─► replica (public reads)
                          │   └──► Redis (rate limits, counter buffers, cache)
                          └─ outbox table ─► cmd/worker ─► search index, sitemaps,
                                                           ISR revalidation, CDN purge
```

- **Content** is Markdown with a whitelist of directives (runnable code, code tabs, callouts,
  problem embeds). Never MDX: nothing executable is stored, any client can render it (including
  the Android app), and parsing stays cheap.
- **Every side effect goes through the transactional outbox.** A change and its event commit
  together; the worker delivers at least once. Handlers must therefore be idempotent.
- **Public responses never vary by user**, so the CDN can serve them to everyone. Per-user
  state (liked? bookmarked?) comes from `/v1/me/...` calls made by client islands.
- **Files never pass through the API.** Clients PUT to presigned URLs. Storage that verifies
  checksums (S3, R2) proves the bytes at upload; otherwise the API hashes small files and the
  worker hashes large ones. Private files (test sets) live in a separate bucket with no URL.
- **Code runs on ls-judge** (`WebstormProjects/ls-judge`), called service-to-service with one
  API key (`JUDGE_URL`, `JUDGE_API_KEY`); browsers never reach the judge. Runs relay results
  through `/v1/run/{id}` (owner checked in Redis). Submissions are queued, split into judge jobs of
  ≤100 cases sent in idempotent batches of ≤20, and judged by the worker.
- **Engagement tables are hash-partitioned** by `content_id` or `user_id`, the key every query
  carries. Those are also the distribution columns if we ever shard with Citus.

## Layout

```
cmd/api, cmd/worker           binaries
internal/<domain>/            handler.go (HTTP), service.go (rules), <domain>db/ (sqlc, generated)
internal/platform/            cross-cutting: config, db, httpx, apperr, ratelimit, testenv
db/migrations/                goose SQL migrations (the schema's source of truth)
db/queries/<domain>.sql       sqlc input; one generated package per domain
api/openapi.yaml              HTTP contract; clients are generated from it
```

Domains never read another domain's tables. They call its service or react to its outbox events.

## Running locally

Prerequisites: Go, Docker, `brew install sqlc goose`.

```sh
cp .env.example .env
make infra-up PROFILES=storage            # Postgres, Redis, SeaweedFS (S3); add "search" later
make storage-init                         # buckets sprout-public (anonymous read) and sprout-private
make migrate
make run-api                              # http://localhost:9300
make run-worker
make seed                                 # optional: sample sections, articles, blog posts, problems
```

## Everyday commands

| Command | What it does |
|---|---|
| `make test` | Creates/migrates `sprout_test`, runs all tests with `-race` (integration tests included) |
| `make generate` | Regenerates sqlc code after editing `db/queries/*.sql` or migrations |
| `make migration name=add_x` | New goose migration file |
| `make lint lint-api` | `go vet` and the OpenAPI lint |
| `make seed` | Sample content through the real services (local databases only; safe to re-run). `go run ./cmd/seed -check` validates the sample bodies without a database |

## Conventions

- **Errors:** services return `apperr` errors with a stable `code`; handlers never format errors
  themselves. Unknown errors become a generic 500 and are logged with the request id.
- **Pagination:** keyset only (opaque cursor of `(sort_key, id)`); OFFSET is never used.
- **Migrations:** plain SQL with an `Up` and a `Down`. Name constraints that clients can hit so
  the service can map them to specific error codes.
- **New monthly-partitioned table:** add it to `internal/maintenance/partitions.go`, or inserts
  start failing once its pre-created months run out.
