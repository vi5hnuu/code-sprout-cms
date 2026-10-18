You have an unlimited supply of coins of each given denomination. Find the fewest coins whose values add up to exactly `amount`. If that is impossible, print `-1`.

## Input

- The first line holds `n`, the number of denominations, and `amount`.
- The second line holds the `n` coin values.

## Output

The minimum number of coins, or `-1`.

## Constraints

- `1 ≤ n ≤ 12`
- `1 ≤ coin ≤ 2^31 - 1`
- `0 ≤ amount ≤ 10^4`

:::callout{kind=warning}
Greedily taking the largest coin first is not always optimal. With coins `1 3 4` and amount `6`, greedy gives `4 + 1 + 1` (3 coins) but `3 + 3` uses only 2.
:::
