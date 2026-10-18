Given a list of closed intervals `[start, end]`, merge every group of overlapping intervals and print the resulting intervals in increasing order of start.

Two intervals overlap when they share at least one point, so `[1, 4]` and `[4, 5]` merge into `[1, 5]`.

## Input

- The first line holds `n`, the number of intervals.
- Each of the next `n` lines holds two integers `start end`.

## Output

One merged interval per line, as `start end`, sorted by `start`.

## Constraints

- `1 ≤ n ≤ 10^4`
- `0 ≤ start ≤ end ≤ 10^4`
