You are given an array of integers `nums` and an integer `target`. Exactly one pair of different positions `i` and `j` has `nums[i] + nums[j] = target`. Print that pair with the smaller index first.

## Input

- The first line holds `n`, the length of the array.
- The second line holds the `n` integers of `nums`, separated by spaces.
- The third line holds `target`.

## Output

Print `i` and `j` (0-based, with `i` before `j`) separated by a space.

## Constraints

- `2 ≤ n ≤ 10^5`
- `-10^9 ≤ nums[k] ≤ 10^9`
- `-2·10^9 ≤ target ≤ 2·10^9`
- Exactly one valid pair exists.

## Follow-up

The obvious solution checks every pair in O(n²) time. Can you find the pair in a single pass? The [hash maps article](/dsa/hash-maps-explained) shows the idea.
