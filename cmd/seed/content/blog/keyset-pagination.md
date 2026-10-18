Open a listing on a large site and jump to page 10,000. On many sites that page takes seconds to load. On Code Sprout it loads as fast as page 1. The difference is one technique: **keyset pagination**.

## Why OFFSET gets slower

The common way to paginate is:

```sql
SELECT * FROM posts ORDER BY published_at DESC LIMIT 20 OFFSET 199980;
```

The database cannot jump to row 199,980. It walks the index from the start, counts 199,980 rows and throws them away, then returns the next 20. Cost grows linearly with the page number, and crawlers that follow every page multiply it.

## Seek, don't skip

Keyset pagination remembers where the previous page ended, its last `(published_at, id)`, and asks for rows after that point:

```sql
SELECT * FROM posts
WHERE (published_at, id) < ($last_published_at, $last_id)
ORDER BY published_at DESC, id DESC
LIMIT 20;
```

With an index on `(published_at DESC, id DESC)`, the database seeks directly to the position in the tree and reads 20 entries. Page 10,000 costs exactly the same as page 1.

The `id` tiebreaker matters. Two posts can share a timestamp, and without a unique final column some rows would be skipped or repeated at page boundaries.

## The trade-offs

- **No "jump to page N".** You can go to the next page, not to an arbitrary one. For browsing feeds, listings and infinite scroll, that is what people actually do. Search engines discover deep pages through sitemaps, not by paging.
- **Opaque cursors.** We encode the last key into a cursor string, so the URL looks like `?cursor=eyJwIjoi…` rather than `?page=500`.
- **Stable under inserts.** New posts at the top do not shift later pages, so nobody sees duplicates while they scroll.

## Beyond pagination

The same principle runs through the whole platform: **no request does work proportional to the total size of the catalogue.** Sitemaps are split into fixed id ranges, so publishing an article only touches the newest chunk. Like and view counters are buffered and written in batches. Pages are rendered once and cached at the edge. Each piece keeps its cost flat as the site grows.
