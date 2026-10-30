# how.md — session changelog with reasoning

This document explains what was changed in Code Sprout CMS during the review-and-fix session, and **why** each change was made the way it was. Written for a future-you who comes back in six months and asks "why did past-me do this?"

Every section links to the file(s) touched and the PR. If you want the one-line summary, each heading has an emoji — 🔴 for correctness/safety, 🟠 for scale, 🟡 for polish, 🟢 for defence in depth.

---

## 1. 🔴 Login brute-force rate limit tightened: 10 → 5 per minute

**Branch**: `sec/tighten-sensitive-rate-limit` in the `auth` repo.
**File**: `src/main/java/solutions/laxmi/auth/configuration/RateLimitProperties.java`.
**Prod**: `/etc/ls-judge/auth.env` → `AUTH_RATE_LIMIT_SENSITIVE_CAPACITY=5`, `AUTH_RATE_LIMIT_SENSITIVE_REFILL=5`.
**PR**: auth #5.

### Why
A security scan against `code-sprout.laxmi.solutions/api/session/login` showed **the first 429 landed at attempt 10**. That is twice the industry baseline — most auth providers (Auth0, Okta, Supabase) cap the "sensitive bucket" at 3–5 per minute per source IP. 10 lets an attacker try 10 passwords per bucket window per IP, which doubles the walk-the-dictionary rate.

### How
- Prod: edited the env file; restarted `ls-judge-auth.service`; verified first 429 now at attempt 5.
- Source: tightened the `RateLimitProperties.java` defaults to match, so a fresh deploy without the env overrides is safe by default.

### Why 5, not 3
A real user mistypes 2–3 times at most during normal sign-in. 5 keeps the normal flow comfortable, 429s an attacker promptly.

---

## 2. 🔴 Partition runway: 3 → 6 months + startup WARN

**Files**: `internal/maintenance/partitions.go`, `db/queries/maintenance.sql`, `internal/maintenance/maintenancedb/maintenance.sql.go`.
**PR**: code-sprout-cms #5.

### Why
`outbox`, `audit_log`, and `content_daily_stats` are RANGE-partitioned by `created_at`, one partition per month. The worker creates **future** partitions on its daily upkeep tick. Before this change: `monthsAhead = 3` and the migration bootstraps 4 months. If the worker crashes and no-one notices for ~45 days, writes into these tables **fail hard** — there's no `DEFAULT` partition on purpose (comment at `migrations/00007_workflow_and_events.sql:32-33` is deliberate).

### How
- `monthsAhead = 6`. Industry baseline for partition runway is 2–3× the maintenance interval; maintenance runs daily and partitions are monthly, so 6 is the right floor. Even if the worker is down for weeks, writes still land in an existing partition.
- Added `CheckRunway()` that runs at worker startup: it counts the future partitions per table and `slog.Warn`s if any is below `minRunwayMonths = 2`. The ensure tick will heal it on its own, but silent self-healing hides a real problem (broken grant, stuck sibling-host cron) for months. Loud log → operator eyes on the problem.

### Industry pattern
TimescaleDB uses `add_retention_policy()` + `add_continuous_aggregate_policy()` with similar guards. AWS Aurora + pg_partman has `premake` set 4+. Our 6 is at the conservative end of the band.

---

## 3. 🔴 `audit_log` retention — now actually bounded

**Files**: `db/migrations/00017_scale_fixes.sql`, `internal/maintenance/audit.go`, `cmd/worker/main.go`.
**PR**: code-sprout-cms #5.

### Why
`outbox` has `drop_processed_outbox_partitions(retention_days)` so processed events age out. `audit_log` had **no counterpart** — it grew forever. At 10k admin-actions / day × years, that's a billion-row partition set no-one planned for.

### How
- Added `drop_old_audit_partitions(retention_days)` mirroring the outbox function: iterate partitions, parse the upper bound, drop whole partitions once their window is older than retention. Instant; no vacuum debt from mass DELETEs.
- Default retention: **2 years** (`auditRetentionDays = 365*2`). Covers DPDPA (no fixed term but 2 years is defensible) and SOC-2 Type II (also 2y).
- Wired into the worker's `maintenance.Every("audit-log-prune", 24h, …)` cron alongside the outbox prune.

### If you need to keep a specific entry longer
Export it from Postgres into cold storage (S3, SIEM) **before** this cron drops it. The audit trail is for operational replay, not for forever-retention of individual rows.

---

## 4. 🔴 Sitemap chunking: survives shard-prefixed IDs

**Files**: `db/migrations/00017_scale_fixes.sql` (ALTER COLUMN), `internal/content/sitemap.go` (ChunkRange + types), `internal/content/contentdb/*` (sqlc types), `db/queries/content.sql` (`::bigint` cast), `internal/content/sitemap_chunk_test.go` (new).
**PR**: code-sprout-cms #5.

### Why
Migration `00011_id_ranges.sql` set content ids to a Snowflake-style scheme: `id = (shard_id << 47) | seq`. Shard 0 ids start at 1; shard 1 ids start at `1 << 47 = 140 737 488 355 328`. The sitemap chunk number was computed as `id / 50_000`:

- shard 0: ids 1..49 999 → chunk 0, next 50 000 ids → chunk 1, … (dense, small numbers)
- shard 1: ids ~140 737 488 355 329 → chunk ~2 814 749 767 106

That chunk number **does not fit in int4**. `sitemap_chunk.chunk` was declared `int`. On shard 1, every upsert would truncate / wrap-around and the sitemap output would be nonsense. Shard 0 works today — this is a critical bug the moment shard 1 is provisioned.

### How
- `ALTER TABLE sitemap_chunk ALTER COLUMN chunk TYPE bigint` (instant on this table — few rows).
- Every `Chunk int32` struct field → `int64`.
- The SQL that computes chunk in the hub queries (`ListCategorySitemapChunks`, `ListTagSitemapChunks`, `ListCollectionSitemapChunks`) now casts to `bigint` explicitly.
- `ChunkRange(id int64) (chunk, lo, hi int64)` — pure helper, unit-testable without a DB.
- `sitemap_chunk_test.go` pins the arithmetic on shards 0, 1, 2, 3 so a future narrowing of any column or struct field fails loudly instead of silently truncating.

### Industry pattern
Snowflake/UUIDv7 bigint IDs are standard (AWS Lambda uses them, GitHub uses them, we use them). The sitemaps.org 50 000 URLs-per-file limit is a hard spec — the global `id/50_000` is the simplest math that stays stable while content grows.

---

## 5. 🟠 Dropped `idx_bookmark_user_list` (redundant)

**File**: `db/migrations/00017_scale_fixes.sql`.
**PR**: code-sprout-cms #5.

### Why
Two indexes on `bookmark`:
- `idx_bookmark_user_recent (user_id, created_at DESC)` — serves every "my bookmarks" query.
- `idx_bookmark_user_list (user_id, list_id, created_at DESC) WHERE list_id IS NOT NULL` — serves "my bookmarks filtered by list".

The second was redundant: any query that filters by `list_id` can use the first index with a residual `WHERE list_id = ?` filter. Keeping both doubled the write amplification on every `INSERT`/`UPDATE` of a bookmark.

### How
`DROP INDEX IF EXISTS idx_bookmark_user_list`. Migration is reversible (down restores it).

### Industry pattern
`pg_stat_user_indexes` typically shows these "almost-redundant" indexes with low scan rates compared to writes — a common Postgres performance review finds and prunes them.

---

## 6. 🟠 `idx_content_title_trgm` is now partial on `deleted_at IS NULL`

**File**: `db/migrations/00017_scale_fixes.sql`.
**PR**: code-sprout-cms #5.

### Why
The GIN index on `content.title` for trigram search was **unconditional** — it indexed soft-deleted rows too. Search queries all filter `deleted_at IS NULL`, so those index entries were never read, just wasted space + write amplification.

### How
`DROP INDEX idx_content_title_trgm; CREATE INDEX idx_content_title_trgm ON content USING gin (title gin_trgm_ops) WHERE deleted_at IS NULL;`. The partial index skips the soft-deleted rows. GIN pages are expensive to maintain, so this is a non-trivial saving at scale.

### Note on future large-table migrations
The migration **does not use `CONCURRENTLY`** because `content` is small on current prod. For any future index change on a multi-GB table, the pattern is:
1. Add the `-- +goose NO TRANSACTION` directive at the top of the migration.
2. Use `CREATE INDEX CONCURRENTLY` / `DROP INDEX CONCURRENTLY`.
3. These do not block writes and can't run inside a transaction.

---

## 7. 🔴 User-id eraser coverage test — GDPR / DPDPA safety net

**File**: `internal/account/eraser_coverage_test.go`.
**PR**: code-sprout-cms #5.

### Why
Account erasure (`DELETE /v1/me`) runs a list of hand-written `EraseFunc` erasers in one transaction (`internal/account/account.go`). If a developer adds a new partitioned-by-user table (`new_feature(user_id, …)`) and forgets to add an eraser, that table's rows **silently survive erasure** — a direct DPDPA Section 13 / GDPR Article 17 violation.

Postgres can't help us here: hash-partitioned tables **can't** have foreign keys to the referencing side (that is, `app_user` can't declare "and when a user is deleted, cascade into every user-partitioned table"). The constraint has to live in code.

### How
An integration test enumerates every `public.*` table with a user-tied column (`user_id`, `author_id`, `editor_id`, `actor_id`, `owner_id`, `reviewer_id`) and asserts each (table, column) pair is in one of:
- `expectedCoveredTables` — allow-listed as covered by a specific eraser (with the file path for grep).
- `nonPersonalColumns` — explicitly declared non-personal (with a reason).

A new user-tied table that lands without being on either list fails the test with a helpful error that tells the developer exactly what to do.

### How to make a failure pass
1. If the column IS personal: write an `EraseUser` query that touches it, register it in `cmd/api/main.go`'s `account.NewService(...)` call, add the table to `expectedCoveredTables`.
2. If the column really isn't personal (e.g. "created by admin" audit metadata that stays with the content): add to `nonPersonalColumns` with a comment explaining why.

### Industry pattern
Uber's "user data wipe-out" framework and Facebook's TAO have similar invariant checks baked into CI. For a solo-dev project, an integration test is the right size.

---

## 8. 🟢 ScribbleMate theme integration (CMS)

**Branch**: `feat/scribblemate-in-cms`.
**Files**: `apps/code-sprout-cms/components/scribble/scribble-wrapper.tsx` (new), `app/layout.tsx`, `next.config.ts` (CSP), `Dockerfile`.
**PR**: laxmi-solutions-nextjs #63 + code-sprout-cms (deploy) #2.

### Why
The legacy `apps/code-sprout` had a ScribbleMate overlay with hard-coded dark colours (`#0c0f1a`, emerald green FAB, white borders). The user asked to bring it into the new CMS **matching the theme** — so it respects light/dark mode and the Sprout brand accent, not the legacy palette.

### How
- Rewrote the wrapper against Tailwind theme tokens (`bg-surface`, `text-fg`, `border-line`, `bg-brand-solid`).
- Removed the inline background on the docked panel + its header after the user flagged that opacity on strokes was being swallowed by the panel's own bg (the overlay now shows through).
- Dropped the `lottie-react` dependency (CMS doesn't pull it in); replaced with a small CSS-only spinner.
- Env-gated on `NEXT_PUBLIC_SCRIBBLEMATE_HOST` — empty env means no FAB and no iframe mount (forks and local builds stay clean).
- CSP `frame-src` picks up the ScribbleMate origin at build time so the iframe loads.

### Pattern
"Floating widget as a sibling of `<Providers>` in the root layout" is standard for overlays. Keeping the iframe `src` behind an env var is the standard way to let CI builds build without live third-party dependencies.

---

## 9. 🟢 Blog SSG on code-sprout-cms

**Branch**: `perf/blogs-ssg`.
**File**: `apps/code-sprout-cms/app/(site)/blog/[slug]/page.tsx`.
**PR**: laxmi-solutions-nextjs #64.

### Why
`/blog/[slug]` was configured for **lazy ISR** (`generateStaticParams` returned `[]`). Every first visit to each blog post paid a server-render cost. With 22 posts just shipped, the first visitors to each were paying that cost; SSG makes the first request a cache hit.

### How
`generateStaticParams` now pages through `listContent({type:'blog', limit:50})` and returns every slug. `dynamicParams = true` stays, so posts published **after** a build still fall through the ISR path on first request + the outbox-driven revalidate wires them in.

### Pattern
This is Next.js's standard SSG+ISR pattern. Pre-render what you can at build time; keep the ISR path open for anything added later.

---

## 10. 🟢 Blog UX — chevron rail + sparse-rail filter + wider sidebar

**Branch**: `fix/videos-sparse-rails`.
**Files**: `components/videos/rail.tsx` (new client component), `components/videos/video-index.tsx` (filter), `components/content/content-page.tsx` (sidebar).
**PR**: laxmi-solutions-nextjs #62 (merged).

### Why
Three unrelated UX regressions on the reader surface:
1. The horizontal video rail had a native OS scrollbar — "ugly and dated" (user).
2. Series rails with only 1–2 items looked broken ("empty right margin").
3. The blog detail page's left section-navigation sidebar was 13 rem (208 px) — too narrow for sibling blog titles, which wrapped and truncated.

### How
1. New `Rail` client component: hides the scrollbar (`[-webkit-scrollbar]:hidden` + Firefox), adds hover-reveal chevron buttons on desktop that scroll by one page (`clientWidth * 0.9`), honours `prefers-reduced-motion`, mask-fades the edges with more content off-screen, hidden on mobile (`md:grid` on the chevron). Touch devices keep native momentum scroll.
2. `VideoIndex` filters out series with fewer than 3 long-form items — a scroller for 1 card looks broken; such series stay reachable on `/series`.
3. Widened the blog reading-page left sidebar from 13 rem → 18 rem. Reading column stays at 42 rem (65-72 CPL readability band); right TOC stays at 14 rem (short lines).

---

## 11. 🟢 ScribbleMate panel bg removed

**File**: `apps/code-sprout-cms/components/scribble/scribble-wrapper.tsx`.
**PR**: laxmi-solutions-nextjs #63 (bundled).

### Why
The docked panel's `bg-surface` was BEHIND the iframe but still showed through transparent pixels in the drawing. Brush opacity disappeared visually.

### How
Removed `bg-surface` from the docked panel and from the header. The border + shadow still give the panel affordance; the drawings now show at their true opacity.

---

## 12. 🟡 `/studio/new` duplicate type grid removed

**File**: `apps/code-sprout-cms/app/studio/new/page.tsx`.
**PR**: laxmi-solutions-nextjs #65 (bundled).

### Why
The page had a two-column layout: left column rendered an **informational list** of content types that looked identical to the **interactive radio grid** on the right (same card chrome, same `border-brand bg-brand-soft` for the "selected" one). Users were clicking the left column and nothing happened.

### How
Removed the whole left column; single centered form with the radio grid alone. Simpler, no visual duplication.

---

## 13. 🔴 Google sign-in blank modal → COOP fix

**File**: `packages/utils/src/security-headers.mjs` + `apps/code-sprout-cms/next.config.ts`.
**PR**: laxmi-solutions-nextjs #65 (bundled).

### Why
`Cross-Origin-Opener-Policy: same-origin` severs `window.opener` for cross-origin popups. GSI opens a popup → user grants access → `/gsi/transform` tries to `postMessage` back to the opener → opener reference is **null** → silent failure, white modal.

### How
Added a `coop` option to the shared `securityHeaders()` helper (`'same-origin'` default, `'same-origin-allow-popups'` opt-in). `code-sprout-cms/next.config.ts` opts in. Other apps that don't rely on OAuth popups keep the stricter default (which still protects against the main window being frame-poked by cross-origin openers; `allow-popups` only loosens the popups-we-opened case).

### Industry pattern
This is the standard COOP setting for sites that use Google Sign-In, Stripe Checkout popup, PayPal popup, etc. Google's own docs call it out. Our previous `same-origin` was defence-in-depth that happened to break the thing it was defending.

---

## 14. 🔴 Studio `?type=` filter honoured (prevents class of bulk-op bugs)

**Branch**: `fix/studio-list-type-filter`.
**Files**: `api/openapi.yaml`, `db/queries/content.sql`, `internal/content/{contentdb/content.sql.go, service.go, handler.go}`, `cmd/seed/seeder.go`.
**PR**: code-sprout-cms #4.

### Why
A session-maintenance script tried to bulk-archive BLOG posts via `GET /v1/studio/content?type=BLOG`. The filter was **silently ignored** — the API returned every content type the caller owns, and the script hit videos / articles / problems too (restored manually, but it should never have been possible).

### How
- OpenAPI: `listMyContent` gains an optional `type` query parameter typed as `ContentType`.
- SQL: `ListAuthorContent` adds `AND (sqlc.narg(type_filter)::text IS NULL OR type = sqlc.narg(type_filter)::text)`. Filter runs in the DB — no post-filter paging surprises.
- Service: `ListMine(ctx, p, filterType, req)` takes an optional type.
- Handler: parses + **validates** `?type=`; unknown value → `400 validation_failed`. Prevents silent fallbacks of the same bug class.

### Why validation matters
If the handler had just silently ignored unknown types, every typo in a client would still return "everything" — the same incident could recur.

---

## 15. 🟢 AdSense coverage

**Branches**: `feat/adsense-coverage-root-domain` (web), `feat/adsense-build-arg` (compose), `policy/code-sprout-adsense` (legal), `feat/adsense` (ls-judge/web).
**PRs**: nextjs #65 (10 commits), code-sprout-cms #3, legal #4, ls-judge #31.

### Why
AdSense had rejected `laxmi.solutions` five times. Two root causes:
1. **No editorial content on the root domain** — just a product catalog + about/contact. AdSense's "low value content" rejection means "we don't see enough original editorial copy".
2. **Education positioning** triggered the stricter children-safety review path.

### How
- Shipped **22 original blog posts** at `laxmi.solutions/blog` via the shared `@libs/blog` system (one post per product + an ls-judge engineering tour).
- Dropped "education" from site tagline, keywords, about page, JSON-LD `applicationCategory`, and the sitewide disclaimer bar.
- Added `ads.txt` to every subdomain (chatcraft, code-editor, code-sprout-cms, cueline, kern, lemma, omni-compiler, ls-judge/web).
- Added AdSense loader + meta on `code-editor`, `code-sprout-cms` (reader surface only — not Studio), `ls-judge/web`.
- CSP widened per-app for AdSense origins (pagead2.googlesyndication.com, googleads.g.doubleclick.net, safeframe, adtrafficquality).
- Legal: rewrote code-sprout privacy policy to disclose AdSense + opt-out routes.
- CMS: new category + 22 posts hard-deleted after moving to the root domain.

### Industry pattern
AdSense's **publisher ID is public** (it appears in the DOM on every ad-serving page); binding it via `NEXT_PUBLIC_*` is correct. Google ties the pub-ID to the domain via `ads.txt`, so an attacker can't copy it to their site and steal revenue — Google refuses to serve ads for a mismatched domain.

---

## 16. 🟡 Security review — nothing exploitable found

Documented in the PR descriptions and chat summary. 17 probes against the live stack: markdown XSS (all blocked), IDOR (404 for non-authorised), S3 presigned URL (SHA + content-type + size all signed), SQLi (pgx parameterised), CSRF (SameSite=Lax + 401), signup/forgot-password enumeration (identical responses), exposed .git/.env (all 404), open redirect (`safeNext` strict). Login brute-force rate was the only finding — fixed (see §1).

---

## Deferred — needs a design discussion before implementing

### Reaction fan-out mirror (audit finding #3)
"My reactions" / "my comments" queries fan out across 32 partitions because the `reaction` and `comment` tables are hash-partitioned by `content_id`, not `user_id`. Workable up to ~100 M reactions; expensive after that.

**Options:**
- **Denormalise**: add a `user_reaction` table partitioned by `user_id`, dual-write via outbox. Industry standard (Facebook TAO, Twitter FanoutService).
- **Accept + instrument**: add a Prometheus metric for "my reactions" latency; defer the fan-out until the metric shows it hurts.

### `count(*)` caching (audit findings #6, #7)
`CountUserBookmarks`, `CountUserSubmissions`, `CountItems` do a full scan of the relevant partitions on every profile view. Options:
- **`user_stats` counter table**, updated transactionally with inserts/deletes. Simple, works at any scale. Needs touching ~10 write sites.
- **Redis + flush** (what Instagram does for likes). Faster, slightly less consistent.

Neither is urgent — the current counts are fast for users with < 1 000 bookmarks. Will become significant at scale.

### Everything else from the audit (findings #11–#17, 18–30)
See the audit report. Each is 2–3 lines; all are "worth noting but not urgent".
