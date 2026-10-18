Binary search is not only for finding values in arrays. Whenever a yes/no question about a number `x` is **monotonic** (false for small `x`, true from some point on), you can binary search for the smallest `x` that makes it true. Problems phrased as "the minimum capacity", "the smallest speed" or "the largest minimum distance" are usually this technique in disguise.

## The template

```python
def smallest_true(lo, hi, feasible):
    """Smallest x in [lo, hi] with feasible(x) True; feasible must be monotonic."""
    while lo < hi:
        mid = (lo + hi) // 2
        if feasible(mid):
            hi = mid          # mid works; maybe something smaller does too
        else:
            lo = mid + 1      # mid fails, and so does everything below it
    return lo
```

The work is in two decisions:

1. **The search range.** `lo` must be a value no answer can go below. `hi` must be a value that certainly works.
2. **The feasibility check**, usually a greedy O(n) simulation.

The total cost is O(n · log(hi - lo)).

## Example: shipping packages within D days

Packages with weights `w` must ship in order, over at most `D` days. Each day you load packages until the next one would exceed the ship's capacity. What is the smallest capacity that finishes in time?

- With capacity `C`, a greedy pass counts the days needed. A larger `C` never needs more days, so the check is monotonic.
- `lo = max(w)`: every package must fit on its own. `hi = sum(w)`: everything ships in one day.

```python runnable title="Minimum ship capacity"
def ship_within_days(weights, days):
    def feasible(cap):
        needed, load = 1, 0
        for w in weights:
            if load + w > cap:
                needed, load = needed + 1, 0
            load += w
        return needed <= days

    lo, hi = max(weights), sum(weights)
    while lo < hi:
        mid = (lo + hi) // 2
        if feasible(mid):
            hi = mid
        else:
            lo = mid + 1
    return lo

print(ship_within_days([1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 5))   # 15
```

## Spotting the pattern

:::details[Signals that a problem is binary search on the answer]
- The question asks for a **minimum or maximum** value, not for a structure.
- Checking a single candidate is easy, but constructing the optimum directly is hard.
- Making the candidate larger (or smaller) can only make it easier to satisfy.
- The constraints allow O(n log V), where V is the range of values (often up to 10^9).
:::

## Common pitfalls

- **Non-monotonic checks.** If `feasible` can flip from true back to false, binary search returns garbage. Prove monotonicity before you code.
- **Wrong bounds.** Starting `lo` at 0 for the shipping problem would accept capacities smaller than the heaviest package.
- **Floating-point answers.** Iterate a fixed number of times (say 100) instead of comparing `lo < hi` with doubles.

Before this, make sure the [basic binary search](/dsa/binary-search) invariants are solid.
