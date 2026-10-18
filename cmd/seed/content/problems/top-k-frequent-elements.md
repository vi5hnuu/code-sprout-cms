Given an integer array `nums` and an integer `k`, print the `k` values that occur most often.

Order the answer by frequency, highest first. When two values occur equally often, print the smaller value first. This makes the answer unique.

## Input

- The first line holds `n` and `k`.
- The second line holds the `n` integers.

## Output

The `k` values, separated by spaces, in the order described above.

## Constraints

- `1 ≤ n ≤ 10^5`
- `-10^4 ≤ nums[i] ≤ 10^4`
- `1 ≤ k ≤` the number of distinct values in `nums`

## Follow-up

Sorting all distinct values costs O(m log m). Since a count is at most `n`, can you bucket values by count instead?
