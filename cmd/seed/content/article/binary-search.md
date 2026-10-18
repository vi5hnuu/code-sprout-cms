Binary search finds a value in a **sorted** array by halving the search range on every step: O(log n) comparisons, about 20 for a million elements. The idea is simple. Getting the boundaries right is where everyone slips. The cure is to choose an invariant and never break it.

## The invariant

Search for `target` in `a[lo..hi]` (both ends inclusive). Hold this invariant: **if the target is in the array, it is inside `[lo, hi]`.**

- Start with `lo = 0`, `hi = n - 1`, so the whole array is in range.
- Look at `mid`. If `a[mid] < target`, everything up to `mid` is too small: set `lo = mid + 1`.
- If `a[mid] > target`, set `hi = mid - 1`.
- Stop when the range is empty (`lo > hi`): the target is absent.

:::code-tabs
```python runnable title="Python" stdin="6\n-1 0 3 5 9 12\n9"
def search(a, target):
    lo, hi = 0, len(a) - 1
    while lo <= hi:
        mid = lo + (hi - lo) // 2
        if a[mid] == target:
            return mid
        if a[mid] < target:
            lo = mid + 1
        else:
            hi = mid - 1
    return -1

n = int(input())
a = list(map(int, input().split()))
print(search(a, int(input())))
```
```java runnable title="Java"
public class Main {
    static int search(int[] a, int target) {
        int lo = 0, hi = a.length - 1;
        while (lo <= hi) {
            int mid = lo + (hi - lo) / 2;      // never overflows
            if (a[mid] == target) return mid;
            if (a[mid] < target) lo = mid + 1;
            else hi = mid - 1;
        }
        return -1;
    }

    public static void main(String[] args) {
        System.out.println(search(new int[]{-1, 0, 3, 5, 9, 12}, 9)); // 4
    }
}
```
```cpp runnable title="C++"
#include <iostream>
#include <vector>
using namespace std;

int search(const vector<int>& a, int target) {
    int lo = 0, hi = (int)a.size() - 1;
    while (lo <= hi) {
        int mid = lo + (hi - lo) / 2;
        if (a[mid] == target) return mid;
        if (a[mid] < target) lo = mid + 1; else hi = mid - 1;
    }
    return -1;
}

int main() { cout << search({-1, 0, 3, 5, 9, 12}, 2) << "\n"; } // -1
```
:::

:::callout[The overflow bug]{kind=danger}
`(lo + hi) / 2` overflows a 32-bit int when both are above about 1.07 billion. It sat unnoticed in Java's own `Arrays.binarySearch` for nine years. Always write `lo + (hi - lo) / 2`.
:::

## Lower bound: the variant you will actually use

Often you need the **first position where `a[i] ≥ target`**: where to insert, how many elements are smaller, or the first occurrence of a duplicate. Use a half-open range `[lo, hi)` and this invariant: everything before `lo` is smaller than the target, and everything from `hi` on is at least the target.

```python
def lower_bound(a, target):
    lo, hi = 0, len(a)            # half-open [lo, hi)
    while lo < hi:
        mid = (lo + hi) // 2
        if a[mid] < target:
            lo = mid + 1          # a[mid] is too small: answer is to the right
        else:
            hi = mid              # a[mid] might be the answer: keep it
    return lo                     # first index with a[i] >= target (or len(a))
```

The standard libraries ship exactly this: Python's `bisect.bisect_left`, C++'s `std::lower_bound` and Java's `Collections.binarySearch` (which returns `-(insertion point) - 1` when the value is absent).

## Three questions to check any binary search

1. Is the range inclusive or half-open, and do the initial `lo`/`hi` match?
2. Does every branch **shrink** the range? `lo = mid` together with `mid = (lo + hi) // 2` can loop forever when `hi = lo + 1`.
3. What does the loop return when the target is missing?

Once this is second nature, try [binary search on the answer](/dsa/binary-search-on-the-answer), where the "array" is a range of possible answers.

::problem{slug=binary-search}
