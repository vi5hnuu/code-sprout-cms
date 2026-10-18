**Dynamic programming** (DP) solves a problem by combining answers to smaller versions of the same problem, and computing each smaller answer only once. It applies whenever a problem has two properties:

- **Optimal substructure**: the answer for `n` can be built from answers for smaller inputs.
- **Overlapping subproblems**: a naive recursion would solve the same smaller inputs again and again.

The best way to learn it is to take one problem through three versions.

## Step 1: write the recurrence

*Climbing stairs*: you can climb 1 or 2 steps at a time. How many ways are there to climb `n` steps?

Think about the **last move**. It was either a single step from `n - 1` or a double step from `n - 2`. So:

```
ways(n) = ways(n - 1) + ways(n - 2),   ways(0) = 1, ways(1) = 1
```

Translated directly into code:

```python
def ways(n):
    if n <= 1:
        return 1
    return ways(n - 1) + ways(n - 2)
```

This is correct but takes exponential time. `ways(40)` makes over 300 million calls, because `ways(38)` is computed twice, `ways(37)` three times, and so on.

## Step 2: memoise (top-down)

Store each answer the first time you compute it:

```python
from functools import cache

@cache
def ways(n):
    if n <= 1:
        return 1
    return ways(n - 1) + ways(n - 2)
```

Each `n` is now computed once, so the cost drops to **O(n)**. Memoisation keeps the shape of the recurrence. It is often the fastest way to a correct solution.

## Step 3: tabulate (bottom-up)

Fill a table from the base cases upwards, so no recursion is needed and the stack cannot overflow:

:::code-tabs
```python runnable title="Python" stdin="10"
def ways(n):
    dp = [1, 1] + [0] * (n - 1)
    for i in range(2, n + 1):
        dp[i] = dp[i - 1] + dp[i - 2]
    return dp[n]

print(ways(int(input())))   # 89 for n = 10
```
```cpp runnable title="C++"
#include <iostream>

long long ways(int n) {
    long long prev = 1, cur = 1;          // ways(0), ways(1)
    for (int i = 2; i <= n; i++) {
        long long next = prev + cur;
        prev = cur;
        cur = next;
    }
    return cur;
}

int main() { std::cout << ways(45) << "\n"; }   // 1836311903
```
```java runnable title="Java"
public class Main {
    static long ways(int n) {
        long prev = 1, cur = 1;
        for (int i = 2; i <= n; i++) {
            long next = prev + cur;
            prev = cur;
            cur = next;
        }
        return cur;
    }

    public static void main(String[] args) { System.out.println(ways(10)); }
}
```
:::

:::callout{kind=tip}
When each entry depends only on the previous few, you can keep only those few, as the C++ and Java versions do. Memory drops from O(n) to O(1).
:::

## A harder recurrence: coin change

What is the fewest coins that make up `amount`? Let `best[a]` be the answer for amount `a`. The last coin used was some `c`, so:

```
best[0] = 0
best[a] = 1 + min(best[a - c]) over every coin c ≤ a
```

```python runnable title="Coin change, bottom-up"
def coin_change(coins, amount):
    INF = float("inf")
    best = [0] + [INF] * amount
    for a in range(1, amount + 1):
        for c in coins:
            if c <= a and best[a - c] + 1 < best[a]:
                best[a] = best[a - c] + 1
    return best[amount] if best[amount] != INF else -1

print(coin_change([1, 2, 5], 11))   # 3
print(coin_change([2], 3))          # -1
```

## The recipe

1. **Define the state** in words: "`best[a]` is the fewest coins for amount `a`."
2. **Write the transition**: how a state follows from smaller ones.
3. **Set the base cases**, then the **order** to fill the table so that dependencies come first.
4. **Optimise space** if you only ever look a few states back.

Next, [Kadane's algorithm](/dsa/kadanes-algorithm) applies the same thinking to arrays.

::problem{slug=climbing-stairs}

::problem{slug=coin-change}
