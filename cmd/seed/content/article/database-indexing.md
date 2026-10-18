Without an index, a query like `SELECT * FROM orders WHERE customer_id = 42` must read **every row** in the table, a sequential scan. With an index on `customer_id`, the database walks a small tree to the matching rows and reads only those. On a table of 100 million rows, that is the difference between seconds and a millisecond.

## The B-tree

Most relational indexes (PostgreSQL, MySQL InnoDB, SQL Server) are **B-trees**: balanced trees whose nodes are disk pages holding hundreds of sorted keys each.

- A lookup reads one page per level. Because each page fans out to hundreds of children, a billion keys fit in about **four levels**, and the top levels stay cached in memory.
- Keys are **sorted**, so range queries (`BETWEEN`, `<`, `ORDER BY ... LIMIT`) walk adjacent leaf pages without scanning the table.

## Composite indexes: order matters

An index on `(customer_id, created_at)` sorts by customer first, then by date within each customer. Think of a phone book sorted by last name, then first name.

| Query | Uses the index? |
|---|---|
| `WHERE customer_id = 42` | ✅ leading column |
| `WHERE customer_id = 42 AND created_at > '2026-01-01'` | ✅ both columns |
| `WHERE customer_id = 42 ORDER BY created_at DESC LIMIT 20` | ✅ no sort needed |
| `WHERE created_at > '2026-01-01'` | ❌ not the leading column |

:::callout{kind=tip}
Put equality columns first and range or sort columns last. An index that matches both the `WHERE` and the `ORDER BY` lets the database stop after reading exactly `LIMIT` rows.
:::

## Covering indexes

If the index contains every column the query needs, the database answers from the index alone, an **index-only scan**, without visiting the table:

```sql
CREATE INDEX orders_customer_recent
    ON orders (customer_id, created_at DESC)
    INCLUDE (total);

-- served entirely from the index
SELECT created_at, total
FROM orders
WHERE customer_id = 42
ORDER BY created_at DESC
LIMIT 20;
```

## Why OFFSET pagination slows down

`LIMIT 20 OFFSET 200000` still reads 200,020 index entries and throws 200,000 away. Deep pages get linearly slower. **Keyset pagination** remembers the last row of the previous page and seeks straight past it:

```sql
SELECT id, created_at, title
FROM posts
WHERE (created_at, id) < ($1, $2)       -- last row of the previous page
ORDER BY created_at DESC, id DESC
LIMIT 20;
```

With an index on `(created_at DESC, id DESC)`, page 10,000 costs the same as page 1. The `id` tiebreaker makes the order total, so no rows are skipped or repeated when two share a timestamp.

## The cost of indexes

Every index speeds up some reads and slows down **every write**, because each insert or update must also update the index. Indexes also take disk and memory. Index for the queries you actually run, check with `EXPLAIN ANALYZE`, and drop indexes nothing uses.

## Partial indexes

When queries only touch a subset of rows, index only that subset:

```sql
CREATE INDEX posts_published_recent
    ON posts (published_at DESC)
    WHERE status = 'PUBLISHED';
```

It is smaller and faster, and drafts never bloat it.
