A cache keeps a copy of data somewhere faster than its source: memory instead of disk, or a nearby edge server instead of the origin. Done well, it absorbs most of the read traffic and cuts latency from tens of milliseconds to under one. Done badly, it serves stale data or collapses under a stampede. Here are the strategies and their trade-offs.

## Cache-aside (lazy loading)

The application owns the logic:

1. Read from the cache.
2. On a miss, read from the database, then write the result to the cache with a TTL.
3. On a write, update the database and **delete** the cache key.

```python
def get_user(user_id):
    key = f"user:{user_id}"
    cached = cache.get(key)
    if cached is not None:
        return cached
    user = db.fetch_user(user_id)
    cache.set(key, user, ttl=300)
    return user

def update_user(user_id, fields):
    db.update_user(user_id, fields)
    cache.delete(f"user:{user_id}")    # next read repopulates it
```

It is simple and resilient: if the cache dies, reads fall through to the database. It is the default choice for most read-heavy services.

:::callout{kind=warning}
Delete the key rather than writing the new value into it. Two concurrent writers can otherwise finish in the opposite order to their database commits and leave the older value cached.
:::

## Write-through

Every write goes to the cache and the database together, so the cache is always warm and consistent for keys that have been written. Writes get slower, and the cache fills with data that may never be read.

## Write-behind (write-back)

Writes go to the cache and are flushed to the database asynchronously, in batches. Writes are very fast and load on the database is smoothed. The price is the risk of losing data if the cache fails before flushing. It suits counters and analytics, not orders or payments.

## Choosing a TTL and an eviction policy

- **TTL**: the longest staleness you can tolerate. Five minutes for a profile, seconds for a price, until-invalidated for immutable content.
- **Eviction**: when memory is full, **LRU** (least recently used) is a good default. LFU keeps popular items longer under scan-heavy workloads.

## Stampedes and hot keys

When a very popular key expires, thousands of requests miss at once and all hit the database. This is a **cache stampede**. Defences:

- **Request coalescing**: only one request recomputes the value, and the others wait for its result.
- **Stale-while-revalidate**: serve the expired value while one background refresh runs. HTTP has this built in: `Cache-Control: s-maxage=60, stale-while-revalidate=600`.
- **Jittered TTLs**: add a random few seconds so keys written together do not expire together.

## Layers of caching

A request to a content site can be answered at several layers, and every layer that answers saves all the ones behind it:

1. the **browser** cache;
2. a **CDN** edge, which handles most anonymous page views on a large content site;
3. the **application** cache, such as rendered pages or Redis;
4. the **database** buffer cache, which keeps hot index and table pages in RAM.

:::callout{kind=tip}
Keep personalised data out of cacheable responses. Serve the same page HTML to everyone from the CDN and load per-user state ("you liked this") in a small separate request. That one rule lets a single cached page serve millions of readers.
:::
