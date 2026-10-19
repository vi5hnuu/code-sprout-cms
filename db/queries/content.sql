-- ── Content rows ─────────────────────────────────────────────────────────────

-- name: InsertContent :one
INSERT INTO content (type, locale, slug, title, summary, visibility, access, author_id, primary_category_id)
VALUES (sqlc.arg(type), sqlc.arg(locale), sqlc.arg(slug), sqlc.arg(title), sqlc.narg(summary),
        sqlc.arg(visibility), sqlc.arg(access), sqlc.arg(author_id), sqlc.narg(primary_category_id))
RETURNING id;

-- name: GetContent :one
SELECT * FROM content WHERE id = $1 AND deleted_at IS NULL;

-- Every workflow change locks the row first, so concurrent saves/publishes serialise and the
-- If-Match check is race-free.
-- name: GetContentForUpdate :one
SELECT * FROM content WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: AllocateRevNo :one
UPDATE content SET last_rev_no = last_rev_no + 1 WHERE id = $1 RETURNING last_rev_no;

-- name: SetDraftRevision :exec
UPDATE content SET draft_rev_no = sqlc.arg(rev_no), updated_at = now() WHERE id = sqlc.arg(id);

-- Never-published content mirrors its draft so the studio lists current titles and the slug
-- is reserved. Published content's public face only changes in PublishContent.
-- name: MirrorDraftFace :exec
UPDATE content
SET slug = sqlc.arg(slug), title = sqlc.arg(title), summary = sqlc.narg(summary),
    primary_category_id = sqlc.narg(primary_category_id), visibility = sqlc.arg(visibility),
    access = sqlc.arg(access), updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'DRAFT';

-- name: SlugTakenByOther :one
SELECT EXISTS (
    SELECT 1 FROM content
    WHERE type = sqlc.arg(type) AND locale = sqlc.arg(locale) AND slug = sqlc.arg(slug)
      AND id <> sqlc.arg(id) AND deleted_at IS NULL
);

-- name: SetReviewState :exec
UPDATE content
SET review_state = sqlc.arg(review_state), scheduled_at = sqlc.narg(scheduled_at), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: PublishContent :one
UPDATE content
SET slug = sqlc.arg(slug), title = sqlc.arg(title), summary = sqlc.narg(summary),
    primary_category_id = sqlc.narg(primary_category_id), visibility = sqlc.arg(visibility),
    access = sqlc.arg(access), status = 'PUBLISHED', published_rev_no = sqlc.arg(rev_no)::int,
    published_at = COALESCE(published_at, now()), last_published_at = now(),
    review_state = 'NONE', scheduled_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetContentStatus :exec
UPDATE content SET status = sqlc.arg(status), updated_at = now() WHERE id = sqlc.arg(id);

-- name: SoftDeleteContent :exec
UPDATE content SET deleted_at = now(), review_state = 'NONE', scheduled_at = NULL, updated_at = now()
WHERE id = $1;

-- ── Revisions ────────────────────────────────────────────────────────────────

-- name: InsertRevision :exec
INSERT INTO content_revision (content_id, rev_no, body_md, body_sha256, meta, editor_id, message)
VALUES (sqlc.arg(content_id), sqlc.arg(rev_no), sqlc.arg(body_md), sqlc.arg(body_sha256),
        sqlc.arg(meta), sqlc.arg(editor_id), sqlc.narg(message));

-- name: GetRevision :one
SELECT * FROM content_revision WHERE content_id = $1 AND rev_no = $2;

-- History without bodies (bodies can be large; fetch one with GetRevision).
-- name: ListRevisions :many
SELECT rev_no, editor_id, message, created_at, (archive_file_id IS NOT NULL)::bool AS archived
FROM content_revision
WHERE content_id = sqlc.arg(content_id)
  AND (sqlc.narg(before_rev)::int IS NULL OR rev_no < sqlc.narg(before_rev)::int)
ORDER BY rev_no DESC
LIMIT sqlc.arg(row_limit);

-- ── Derived data written at publish ─────────────────────────────────────────

-- name: UpsertRender :exec
INSERT INTO content_render (content_id, rev_no, toc, plain_text, word_count, reading_minutes, snippet_count, rendered_at)
VALUES (sqlc.arg(content_id), sqlc.arg(rev_no), sqlc.arg(toc), sqlc.arg(plain_text), sqlc.arg(word_count),
        sqlc.arg(reading_minutes), sqlc.arg(snippet_count), now())
ON CONFLICT (content_id) DO UPDATE
SET rev_no = EXCLUDED.rev_no, toc = EXCLUDED.toc, plain_text = EXCLUDED.plain_text,
    word_count = EXCLUDED.word_count, reading_minutes = EXCLUDED.reading_minutes,
    snippet_count = EXCLUDED.snippet_count, rendered_at = now();

-- name: GetRender :one
SELECT * FROM content_render WHERE content_id = $1;

-- name: DeleteContentAssets :exec
DELETE FROM content_asset WHERE content_id = $1;

-- name: InsertContentAssets :exec
INSERT INTO content_asset (content_id, file_id)
SELECT sqlc.arg(content_id), unnest(sqlc.arg(file_ids)::bigint[]);

-- name: DeleteContentTags :exec
DELETE FROM content_tag WHERE content_id = $1;

-- name: InsertContentTags :exec
INSERT INTO content_tag (tag_id, content_id, published_at)
SELECT unnest(sqlc.arg(tag_ids)::bigint[]), sqlc.arg(content_id), sqlc.narg(published_at);

-- Unpublish/archive takes the item out of tag listings without losing its tags.
-- name: SetContentTagsPublishedAt :exec
UPDATE content_tag SET published_at = sqlc.narg(published_at) WHERE content_id = sqlc.arg(content_id);

-- name: ListContentTagIDs :many
SELECT tag_id FROM content_tag WHERE content_id = $1 ORDER BY tag_id;

-- name: DeleteLinksOfKind :exec
DELETE FROM content_link WHERE from_id = sqlc.arg(from_id) AND kind = sqlc.arg(kind);

-- name: InsertLinks :exec
INSERT INTO content_link (from_id, kind, to_id, position)
SELECT sqlc.arg(from_id), sqlc.arg(kind), t.id, t.pos::int
FROM unnest(sqlc.arg(to_ids)::bigint[]) WITH ORDINALITY AS t(id, pos)
ON CONFLICT DO NOTHING;

-- name: LiveIDsBySlug :many
SELECT id, slug FROM content
WHERE type = sqlc.arg(type) AND locale = sqlc.arg(locale) AND slug = ANY (sqlc.arg(slugs)::text[])
  AND status = 'PUBLISHED' AND deleted_at IS NULL;

-- name: UpsertSlugRedirect :exec
INSERT INTO slug_redirect (type, locale, old_slug, content_id)
VALUES (sqlc.arg(type), sqlc.arg(locale), sqlc.arg(old_slug), sqlc.arg(content_id))
ON CONFLICT (type, locale, old_slug) DO UPDATE SET content_id = EXCLUDED.content_id, created_at = now();

-- A slug coming back into use stops redirecting.
-- name: DeleteSlugRedirect :exec
DELETE FROM slug_redirect WHERE type = sqlc.arg(type) AND locale = sqlc.arg(locale) AND old_slug = sqlc.arg(slug);

-- name: UpsertArticleExt :exec
INSERT INTO article_ext (content_id, level) VALUES ($1, $2)
ON CONFLICT (content_id) DO UPDATE SET level = EXCLUDED.level;

-- name: UpsertBlogExt :exec
INSERT INTO blog_ext (content_id, cover_file_id) VALUES ($1, $2)
ON CONFLICT (content_id) DO UPDATE SET cover_file_id = EXCLUDED.cover_file_id;

-- name: InsertStats :exec
INSERT INTO content_stats (content_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- ── Studio listings (keyset) ────────────────────────────────────────────────

-- name: ListAuthorContent :many
SELECT * FROM content
WHERE author_id = sqlc.arg(author_id) AND deleted_at IS NULL
  AND (sqlc.narg(after_updated)::timestamptz IS NULL
       OR (updated_at, id) < (sqlc.narg(after_updated)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- Oldest-waiting first. The grant join is a row-level authorization filter: it has to live in
-- SQL, because filtering after the LIMIT would return short or empty pages.
-- name: ListReviewQueue :many
SELECT c.* FROM content c
WHERE c.review_state = ANY (sqlc.arg(states)::text[]) AND c.deleted_at IS NULL
  AND (sqlc.narg(after_updated)::timestamptz IS NULL
       OR (c.updated_at, c.id) > (sqlc.narg(after_updated)::timestamptz, sqlc.narg(after_id)::bigint))
  AND (sqlc.arg(is_admin)::bool OR EXISTS (
        SELECT 1 FROM cms_role_grant g
        LEFT JOIN category scope ON scope.id = g.scope_category_id
        LEFT JOIN category cat ON cat.id = c.primary_category_id
        WHERE g.user_id = sqlc.arg(user_id) AND g.role = ANY (sqlc.arg(roles)::text[])
          AND (g.scope_category_id IS NULL OR cat.path <@ scope.path)))
ORDER BY c.updated_at, c.id
LIMIT sqlc.arg(row_limit);

-- name: ClaimDueScheduled :many
SELECT id FROM content
WHERE review_state = 'SCHEDULED' AND scheduled_at <= now() AND deleted_at IS NULL
ORDER BY scheduled_at
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;

-- ── Reviews ─────────────────────────────────────────────────────────────────

-- name: InsertReview :one
INSERT INTO review (content_id, rev_no, reviewer_id, decision, summary)
VALUES (sqlc.arg(content_id), sqlc.arg(rev_no), sqlc.arg(reviewer_id), sqlc.arg(decision), sqlc.narg(summary))
RETURNING id, created_at;

-- name: InsertReviewNote :exec
INSERT INTO review_note (review_id, anchor, body) VALUES (sqlc.arg(review_id), sqlc.arg(anchor), sqlc.arg(body));

-- name: ListReviews :many
SELECT * FROM review WHERE content_id = $1 ORDER BY created_at DESC LIMIT 50;

-- name: ListReviewNotes :many
SELECT * FROM review_note WHERE review_id = ANY (sqlc.arg(review_ids)::bigint[]) ORDER BY id;

-- ── Public reads (replica) ──────────────────────────────────────────────────

-- UNLISTED items resolve by URL; PRIVATE ones never do.
-- name: GetLiveBySlug :one
SELECT * FROM content
WHERE type = sqlc.arg(type) AND locale = sqlc.arg(locale) AND slug = sqlc.arg(slug)
  AND status = 'PUBLISHED' AND visibility <> 'PRIVATE' AND deleted_at IS NULL;

-- Where an old slug now points, so the page can answer with a permanent redirect.
-- name: ResolveRedirect :one
SELECT c.slug FROM slug_redirect r
JOIN content c ON c.id = r.content_id
WHERE r.type = sqlc.arg(type) AND r.locale = sqlc.arg(locale) AND r.old_slug = sqlc.arg(slug)
  AND c.status = 'PUBLISHED' AND c.visibility <> 'PRIVATE' AND c.deleted_at IS NULL;

-- name: ListLiveByType :many
SELECT * FROM content
WHERE type = sqlc.arg(type) AND locale = sqlc.arg(locale)
  AND status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (published_at, id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY published_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListLiveByCategory :many
SELECT * FROM content
WHERE primary_category_id = sqlc.arg(category_id)
  AND status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL
  AND (sqlc.narg(type)::text IS NULL OR type = sqlc.narg(type)::text)
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (published_at, id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY published_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- Driven by the (tag_id, published_at, content_id) index; the join only fetches the rows.
-- name: ListLiveByTag :many
SELECT c.* FROM content_tag ct
JOIN content c ON c.id = ct.content_id
WHERE ct.tag_id = sqlc.arg(tag_id) AND ct.published_at IS NOT NULL
  AND (sqlc.narg(type)::text IS NULL OR c.type = sqlc.narg(type)::text)
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (ct.published_at, ct.content_id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY ct.published_at DESC, ct.content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListLiveByAuthor :many
SELECT * FROM content
WHERE author_id = sqlc.arg(author_id)
  AND status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (published_at, id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY published_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListRenders :many
SELECT content_id, reading_minutes, word_count FROM content_render WHERE content_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: ListContentAssetIDs :many
SELECT file_id FROM content_asset WHERE content_id = $1;

-- Live targets of an item's links (e.g. its practice problems), in authored order.
-- name: ListLiveLinks :many
SELECT l.kind, c.id, c.type, c.locale, c.slug, c.title,
       COALESCE(split_part(cat.path::text, '.', 1), '')::text AS section
FROM content_link l
JOIN content c ON c.id = l.to_id
LEFT JOIN category cat ON cat.id = c.primary_category_id
WHERE l.from_id = $1 AND c.status = 'PUBLISHED' AND c.visibility <> 'PRIVATE' AND c.deleted_at IS NULL
ORDER BY l.kind, l.position;

-- Live items that link to this one with a kind (e.g. the problems a video solves), newest first.
-- Reads idx_content_link_to.
-- name: ListLiveBacklinks :many
SELECT c.id, c.type, c.locale, c.slug, c.title,
       COALESCE(split_part(cat.path::text, '.', 1), '')::text AS section
FROM content_link l
JOIN content c ON c.id = l.from_id
LEFT JOIN category cat ON cat.id = c.primary_category_id
WHERE l.to_id = sqlc.arg(to_id) AND l.kind = sqlc.arg(kind)
  AND c.status = 'PUBLISHED' AND c.visibility <> 'PRIVATE' AND c.deleted_at IS NULL
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit);

-- Live problems whose normalised title is one of these phrases (exact-title matching of
-- videos to problems). Reads idx_content_problem_title.
-- name: LiveProblemsTitled :many
SELECT id, norm_title(title)::text AS norm FROM content
WHERE norm_title(title) = ANY (sqlc.arg(phrases)::text[])
  AND type = 'PROBLEM' AND status = 'PUBLISHED' AND deleted_at IS NULL;

-- Of these items, those that already link to a live item with this kind (a link to something
-- taken down does not count, so it can be replaced).
-- name: WithLinkOfKind :many
SELECT DISTINCT l.from_id FROM content_link l
JOIN content t ON t.id = l.to_id
WHERE l.from_id = ANY (sqlc.arg(ids)::bigint[]) AND l.kind = sqlc.arg(kind)
  AND t.status = 'PUBLISHED' AND t.deleted_at IS NULL;

-- name: GetArticleExt :one
SELECT * FROM article_ext WHERE content_id = $1;

-- name: GetBlogExt :one
SELECT * FROM blog_ext WHERE content_id = $1;

-- Live covers for a page of listing items (blog_ext is written at publish).
-- name: ListBlogCovers :many
SELECT content_id, cover_file_id::bigint AS cover_file_id
FROM blog_ext
WHERE content_id = ANY (sqlc.arg(ids)::bigint[]) AND cover_file_id IS NOT NULL;

-- Live items among ids (UNLISTED included: readers can like a page they reached by URL).
-- name: ListLiveByIDs :many
SELECT * FROM content
WHERE id = ANY (sqlc.arg(ids)::bigint[]) AND status = 'PUBLISHED' AND visibility <> 'PRIVATE' AND deleted_at IS NULL;

-- The most recent live members of one tag; related-item scoring samples each of an item's
-- tags with this, which bounds the cost even for tags with millions of members.
-- name: RecentInTag :many
SELECT content_id FROM content_tag
WHERE tag_id = sqlc.arg(tag_id) AND published_at IS NOT NULL
ORDER BY published_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- Smallest and largest live item id carrying a tag (random picks within a tag).
-- name: TagIDBounds :one
SELECT coalesce(min(content_id), 0)::bigint AS lo, coalesce(max(content_id), 0)::bigint AS hi FROM content_tag
WHERE tag_id = $1 AND published_at IS NOT NULL;

-- Live items of one type carrying a tag, in id order from a pivot: walks the content_tag
-- primary key, so each batch costs the same however large the tag is.
-- name: TaggedOfTypeFrom :many
SELECT ct.content_id FROM content_tag ct
JOIN content c ON c.id = ct.content_id
WHERE ct.tag_id = sqlc.arg(tag_id) AND ct.published_at IS NOT NULL AND ct.content_id >= sqlc.arg(pivot)
  AND c.type = sqlc.arg(type)
ORDER BY ct.content_id
LIMIT sqlc.arg(row_limit);

-- Everything search needs for one live item, in one round trip.
-- name: GetIndexable :one
SELECT c.id, c.type, c.locale, c.slug, c.title, c.summary, c.access, c.visibility, c.status,
       c.published_at, c.last_published_at, c.deleted_at,
       COALESCE(r.plain_text, '')::text AS plain_text,
       COALESCE(cat.path::text, '')::text AS category_path,
       COALESCE(cat.name, '')::text AS category_name,
       COALESCE((SELECT array_agg(t.name ORDER BY t.name) FROM content_tag ct JOIN tag t ON t.id = ct.tag_id
                 WHERE ct.content_id = c.id), '{}')::text[] AS tag_names
FROM content c
LEFT JOIN content_render r ON r.content_id = c.id
LEFT JOIN category cat ON cat.id = c.primary_category_id
WHERE c.id = $1;

-- Keyset walk over live public items, for full reindexes and reconciliation.
-- name: ListLiveIDsAfter :many
SELECT id FROM content
WHERE status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- Keyset walk over live public items filed anywhere under one category (its subtree, by the
-- category's current path), for re-indexing after the category is renamed or moved.
-- name: ListLiveIDsInCategoryAfter :many
SELECT c.id FROM content c
JOIN category cat ON cat.id = c.primary_category_id
WHERE cat.path <@ (SELECT root.path FROM category root WHERE root.id = sqlc.arg(category_id))
  AND c.status = 'PUBLISHED' AND c.visibility = 'PUBLIC' AND c.deleted_at IS NULL AND c.id > sqlc.arg(after_id)
ORDER BY c.id
LIMIT sqlc.arg(row_limit);

-- ── Sitemaps ────────────────────────────────────────────────────────────────

-- Live public items in one id-range chunk (index-only on idx_content_sitemap).
-- name: CountSitemapRange :one
SELECT count(*)::int FROM content
WHERE type = sqlc.arg(type) AND id >= sqlc.arg(lo) AND id < sqlc.arg(hi)
  AND status = 'PUBLISHED' AND visibility = 'PUBLIC' AND deleted_at IS NULL;

-- name: UpsertSitemapChunk :exec
INSERT INTO sitemap_chunk (type, chunk, url_count, lastmod) VALUES (sqlc.arg(type), sqlc.arg(chunk), sqlc.arg(url_count), now())
ON CONFLICT (type, chunk) DO UPDATE SET url_count = EXCLUDED.url_count, lastmod = now();

-- name: ListSitemapChunks :many
SELECT * FROM sitemap_chunk WHERE url_count > 0 ORDER BY type, chunk;

-- The root category slug builds article URLs (/{section}/{slug}).
-- name: ListSitemapEntries :many
SELECT c.id, c.locale, c.slug, c.last_published_at, COALESCE(split_part(cat.path::text, '.', 1), '')::text AS section
FROM content c
LEFT JOIN category cat ON cat.id = c.primary_category_id
WHERE c.type = sqlc.arg(type) AND c.id >= sqlc.arg(lo) AND c.id < sqlc.arg(hi)
  AND c.status = 'PUBLISHED' AND c.visibility = 'PUBLIC' AND c.deleted_at IS NULL
ORDER BY c.id;

-- Hub pages (categories, tags with live content, public editorial collections) are chunked by
-- id range exactly like content. The chunk lists are small aggregates, cached for an hour.
-- name: ListCategorySitemapChunks :many
SELECT (id / 50000)::int AS chunk, count(*)::int AS url_count, max(updated_at)::timestamptz AS lastmod
FROM category GROUP BY 1 ORDER BY 1;

-- A category page changes when the category does or when something is published into it.
-- name: ListCategorySitemapEntries :many
SELECT cat.path::text AS path,
       GREATEST(cat.updated_at, COALESCE((
           SELECT max(c.published_at) FROM content c
           WHERE c.primary_category_id = cat.id
             AND c.status = 'PUBLISHED' AND c.visibility = 'PUBLIC' AND c.deleted_at IS NULL), cat.updated_at))::timestamptz AS lastmod
FROM category cat
WHERE cat.id >= sqlc.arg(lo) AND cat.id < sqlc.arg(hi)
ORDER BY cat.id;

-- Tags without live content are left out: an empty listing is a thin page.
-- name: ListTagSitemapChunks :many
SELECT (t.id / 50000)::int AS chunk, count(*)::int AS url_count, max(l.last)::timestamptz AS lastmod
FROM tag t
CROSS JOIN LATERAL (SELECT max(ct.published_at) AS last FROM content_tag ct WHERE ct.tag_id = t.id AND ct.published_at IS NOT NULL) l
WHERE l.last IS NOT NULL
GROUP BY 1 ORDER BY 1;

-- name: ListTagSitemapEntries :many
SELECT t.kind, t.slug, l.last::timestamptz AS lastmod
FROM tag t
CROSS JOIN LATERAL (SELECT max(ct.published_at) AS last FROM content_tag ct WHERE ct.tag_id = t.id AND ct.published_at IS NOT NULL) l
WHERE t.id >= sqlc.arg(lo) AND t.id < sqlc.arg(hi) AND l.last IS NOT NULL
ORDER BY t.id;

-- name: ListCollectionSitemapChunks :many
SELECT (id / 50000)::int AS chunk, count(*)::int AS url_count, max(updated_at)::timestamptz AS lastmod
FROM collection WHERE kind <> 'LIST' AND visibility = 'PUBLIC'
GROUP BY 1 ORDER BY 1;

-- name: ListCollectionSitemapEntries :many
SELECT kind, slug, updated_at FROM collection
WHERE kind <> 'LIST' AND visibility = 'PUBLIC' AND id >= sqlc.arg(lo) AND id < sqlc.arg(hi)
ORDER BY id;

-- name: GetPathParts :one
SELECT c.type, c.locale, c.slug, COALESCE(split_part(cat.path::text, '.', 1), '')::text AS section
FROM content c LEFT JOIN category cat ON cat.id = c.primary_category_id
WHERE c.id = $1;

-- ── Revision archiving ──────────────────────────────────────────────────────

-- Old bodies that are neither the draft, the live revision, nor among the latest `keep`.
-- name: ListArchivableRevisions :many
SELECT r.content_id, r.rev_no, r.body_md
FROM content_revision r
JOIN content c ON c.id = r.content_id
WHERE r.body_md IS NOT NULL AND r.created_at < sqlc.arg(before)
  AND r.rev_no <= c.draft_rev_no - sqlc.arg(keep)::int
  AND r.rev_no IS DISTINCT FROM c.published_rev_no
ORDER BY r.created_at
LIMIT sqlc.arg(row_limit);

-- name: MarkRevisionArchived :exec
UPDATE content_revision SET body_md = NULL, archive_file_id = sqlc.arg(file_id)
WHERE content_id = sqlc.arg(content_id) AND rev_no = sqlc.arg(rev_no) AND body_md IS NOT NULL;
