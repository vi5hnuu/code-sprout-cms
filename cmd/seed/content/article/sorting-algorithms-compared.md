You will rarely write a sort in production, since every standard library has an excellent one. Knowing how the main algorithms behave still matters. It tells you when sorting first makes a problem easy, what `sort()` costs, and why stability can change your output.

## At a glance

| Algorithm | Best | Average | Worst | Extra space | Stable |
|---|---|---|---|---|---|
| Insertion sort | O(n) | O(n²) | O(n²) | O(1) | Yes |
| Merge sort | O(n log n) | O(n log n) | O(n log n) | O(n) | Yes |
| Quicksort | O(n log n) | O(n log n) | O(n²) | O(log n) | No |
| Heapsort | O(n log n) | O(n log n) | O(n log n) | O(1) | No |
| Counting sort | O(n + k) | O(n + k) | O(n + k) | O(k) | Yes |

No comparison sort can beat O(n log n) in the worst case: there are `n!` possible orderings, and each comparison at best halves them. Counting sort escapes the bound by never comparing elements, but it only works for small integer keys in a range `k`.

## Insertion sort

Grow a sorted prefix by inserting each new element into place. It is quadratic in general, but very fast on tiny or nearly sorted arrays. That is why real libraries switch to it for small slices.

```python
def insertion_sort(a):
    for i in range(1, len(a)):
        x, j = a[i], i - 1
        while j >= 0 and a[j] > x:
            a[j + 1] = a[j]
            j -= 1
        a[j + 1] = x
```

## Merge sort

Split in half, sort each half recursively, then merge the two sorted halves in linear time. It is predictable (always O(n log n)) and stable. Its cost is O(n) extra memory.

```python runnable title="Merge sort"
def merge_sort(a):
    if len(a) <= 1:
        return a
    mid = len(a) // 2
    left, right = merge_sort(a[:mid]), merge_sort(a[mid:])
    out, i, j = [], 0, 0
    while i < len(left) and j < len(right):
        if left[i] <= right[j]:        # <= keeps equal keys in order: stable
            out.append(left[i]); i += 1
        else:
            out.append(right[j]); j += 1
    return out + left[i:] + right[j:]

print(merge_sort([5, 2, 9, 1, 5, 6]))
```

## Quicksort

Pick a pivot, partition the array into smaller and larger elements, and recurse on each side. It works in place and is cache-friendly, so it is usually the fastest in practice. A bad pivot on already sorted input degrades it to O(n²), so implementations pick random or median-of-three pivots.

## What your language actually uses

- **Python** (`sorted`, `list.sort`) and **Java** for objects use **Timsort**: merge sort plus insertion sort, tuned to exploit runs that are already sorted. It is stable.
- **Java** for primitives uses **dual-pivot quicksort**. It is not stable, which is fine because equal primitives are indistinguishable.
- **C++** `std::sort` uses **introsort**: quicksort that falls back to heapsort when recursion gets too deep, plus insertion sort for small ranges. It is not stable. Use `std::stable_sort` when you need stability.

:::callout[Why stability matters]{kind=info}
Sort orders by date, then stable-sort them by customer. The orders of each customer stay in date order. An unstable sort may shuffle them.
:::

## Sorting as a problem-solving step

Sorting first often turns a hard problem into a linear scan:

- **Merge intervals**: after sorting by start, overlapping intervals are neighbours.
- **Pair sums**: sorted input enables [two pointers](/dsa/two-pointers-technique).
- **Duplicates and grouping**: equal elements become adjacent.

```python runnable title="Merging intervals after a sort"
def merge(intervals):
    intervals.sort(key=lambda p: p[0])
    merged = []
    for start, end in intervals:
        if merged and start <= merged[-1][1]:
            merged[-1][1] = max(merged[-1][1], end)
        else:
            merged.append([start, end])
    return merged

print(merge([[1, 3], [8, 10], [2, 6], [15, 18]]))   # [[1, 6], [8, 10], [15, 18]]
```

::problem{slug=merge-intervals}

::problem{slug=top-k-frequent-elements}
