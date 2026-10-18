**Maximum subarray sum**: given an array that may contain negative numbers, find the contiguous subarray with the largest sum. Checking every subarray is O(n²) (or O(n³) done naively). **Kadane's algorithm** solves it in one O(n) pass. It is a tiny dynamic program.

## The key observation

Let `best_ending_here[i]` be the largest sum of a subarray that **ends exactly at index `i`**. That subarray either:

- extends the best subarray ending at `i - 1`, giving `best_ending_here[i - 1] + a[i]`, or
- starts fresh at `i`, giving just `a[i]`.

Starting fresh wins exactly when the running sum has gone negative, because a negative prefix only drags the total down. The answer is the maximum of `best_ending_here` over all `i`.

:::code-tabs
```python runnable title="Python" stdin="9\n-2 1 -3 4 -1 2 1 -5 4"
def max_subarray(a):
    cur = best = a[0]
    for x in a[1:]:
        cur = max(x, cur + x)     # extend, or start fresh at x
        best = max(best, cur)
    return best

n = int(input())
print(max_subarray(list(map(int, input().split()))))   # 6
```
```javascript runnable title="JavaScript"
function maxSubarray(a) {
    let cur = a[0], best = a[0];
    for (let i = 1; i < a.length; i++) {
        cur = Math.max(a[i], cur + a[i]);
        best = Math.max(best, cur);
    }
    return best;
}

console.log(maxSubarray([-2, 1, -3, 4, -1, 2, 1, -5, 4])); // 6
console.log(maxSubarray([-3, -1, -2]));                    // -1
```
```cpp runnable title="C++"
#include <algorithm>
#include <iostream>
#include <vector>
using namespace std;

long long maxSubarray(const vector<long long>& a) {
    long long cur = a[0], best = a[0];
    for (size_t i = 1; i < a.size(); i++) {
        cur = max(a[i], cur + a[i]);
        best = max(best, cur);
    }
    return best;
}

int main() { cout << maxSubarray({5, -9, 6, -2, 3}) << "\n"; } // 7
```
:::

## Walking through an example

| i | a[i] | cur (best ending here) | best so far |
|---|---|---|---|
| 0 | -2 | -2 | -2 |
| 1 | 1 | 1 (start fresh) | 1 |
| 2 | -3 | -2 | 1 |
| 3 | 4 | 4 (start fresh) | 4 |
| 4 | -1 | 3 | 4 |
| 5 | 2 | 5 | 5 |
| 6 | 1 | 6 | 6 |
| 7 | -5 | 1 | 6 |
| 8 | 4 | 5 | 6 |

:::callout{kind=warning}
Initialise with `a[0]`, not 0. Starting at 0 silently returns 0 for an all-negative array, but the problem requires a non-empty subarray.
:::

## Getting the subarray itself

Remember where the current run started, and save it whenever `best` improves:

```python
def max_subarray_range(a):
    cur, best = a[0], a[0]
    start = best_l = best_r = 0
    for i in range(1, len(a)):
        if a[i] > cur + a[i]:
            cur, start = a[i], i
        else:
            cur += a[i]
        if cur > best:
            best, best_l, best_r = cur, start, i
    return best, best_l, best_r
```

## Variations

- **Maximum circular subarray**: the answer is either a normal maximum subarray or the total minus the *minimum* subarray.
- **Maximum product subarray**: track both the maximum and the minimum ending here, because two negatives multiply into a positive.

::problem{slug=maximum-subarray}
