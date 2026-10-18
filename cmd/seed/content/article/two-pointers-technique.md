The **two pointers** technique walks two indices through a sequence, moving each according to a simple rule. Many problems that look like they need two nested loops become a single O(n) pass with O(1) extra space.

There are two common shapes:

- **Opposite ends**: one pointer starts at the left, one at the right, and they move towards each other.
- **Same direction**: a fast pointer explores while a slow pointer marks where to write or where a window starts.

## Opposite ends: pair sum in a sorted array

In a **sorted** array, look at `a[left] + a[right]`:

- If the sum is too small, the only way to grow it is to move `left` right.
- If it is too large, move `right` left.

Each step discards a value that can never be part of the answer, so the loop runs at most `n` times.

:::code-tabs
```python runnable title="Python"
def pair_with_sum(a, target):
    left, right = 0, len(a) - 1
    while left < right:
        s = a[left] + a[right]
        if s == target:
            return left, right
        if s < target:
            left += 1
        else:
            right -= 1
    return None

print(pair_with_sum([1, 3, 4, 6, 8, 11], 10))   # (2, 3): 4 + 6 = 10
```
```cpp runnable title="C++"
#include <iostream>
#include <vector>
using namespace std;

pair<int, int> pairWithSum(const vector<int>& a, int target) {
    int left = 0, right = (int)a.size() - 1;
    while (left < right) {
        int s = a[left] + a[right];
        if (s == target) return {left, right};
        if (s < target) left++; else right--;
    }
    return {-1, -1};
}

int main() {
    auto [i, j] = pairWithSum({1, 3, 4, 6, 8, 11}, 10);
    cout << i << " " << j << "\n";
}
```
:::

:::callout{kind=note}
This only works because the array is sorted. On unsorted input, use a [hash map](/dsa/hash-maps-explained) instead, or sort first in O(n log n) if you do not need the original indices.
:::

## Opposite ends: checking a palindrome

```python
def is_palindrome(s):
    i, j = 0, len(s) - 1
    while i < j:
        if s[i] != s[j]:
            return False
        i, j = i + 1, j - 1
    return True
```

## Same direction: removing duplicates in place

Given a sorted array, keep one copy of each value in its prefix and return the new length. The `write` pointer marks the end of the kept prefix. The `read` pointer scans every element.

```python
def dedupe(a):
    if not a:
        return 0
    write = 1
    for read in range(1, len(a)):
        if a[read] != a[write - 1]:
            a[write] = a[read]
            write += 1
    return write
```

Same-direction pointers grow into the [sliding window technique](/dsa/sliding-window-technique), where the two pointers bound a window whose contents you track.

## Recognising a two-pointer problem

Reach for two pointers when:

- the input is sorted, or sorting it does not lose information you need;
- you are looking for a **pair** or **triplet** with a property (sum, difference);
- you must modify an array **in place** with O(1) extra space;
- you compare a sequence with its own reverse.

::problem{slug=merge-intervals}
