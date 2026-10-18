An **array** stores elements of the same type in one contiguous block of memory. That single property explains almost everything about how arrays behave: which operations are fast, which are slow, and why they are the default data structure in nearly every language.

## How an array sits in memory

If an array of 4-byte integers starts at address `1000`, element `i` lives at address `1000 + 4·i`. Reading `a[7]` is one multiplication and one addition, no matter how long the array is. That is why indexing is **O(1)**.

Contiguity also makes arrays fast in practice. When the CPU loads `a[0]`, it pulls a whole cache line (typically 64 bytes) into its cache, so the next fifteen integers are already there when you need them. Linked structures scatter their nodes across memory and lose this advantage.

## What each operation costs

| Operation | Cost | Why |
|---|---|---|
| Read or write `a[i]` | O(1) | Address arithmetic |
| Append at the end (dynamic array) | O(1) amortised | Occasional resize, see below |
| Insert or delete at position `i` | O(n - i) | Every later element shifts by one |
| Search for a value (unsorted) | O(n) | You may have to look at everything |
| Search for a value (sorted) | O(log n) | [Binary search](/dsa/binary-search) |

## Dynamic arrays

Fixed-size arrays cannot grow. Python's `list`, Java's `ArrayList`, C++'s `std::vector` and JavaScript arrays are **dynamic arrays**: when the backing block fills up, they allocate a bigger one (usually 1.5× or 2× the size) and copy everything across.

A single resize costs O(n), but it happens so rarely that the average cost per append stays constant. Doubling from 1 to 1,024 elements copies 1 + 2 + 4 + … + 512 = 1,023 elements in total, about one copy per append.

:::code-tabs
```python runnable title="Python"
nums = []
for i in range(5):
    nums.append(i * i)        # amortised O(1)
print(nums)                   # [0, 1, 4, 9, 16]
print(nums[3])                # O(1) indexing -> 9
nums.insert(0, -1)            # O(n): every element shifts right
print(nums)
```
```cpp runnable title="C++"
#include <iostream>
#include <vector>

int main() {
    std::vector<int> nums;
    for (int i = 0; i < 5; i++) nums.push_back(i * i);
    std::cout << nums[3] << "\n";            // 9
    nums.insert(nums.begin(), -1);           // shifts every element
    std::cout << nums.size() << " elements, capacity " << nums.capacity() << "\n";
}
```
```java runnable title="Java"
import java.util.ArrayList;

public class Main {
    public static void main(String[] args) {
        ArrayList<Integer> nums = new ArrayList<>();
        for (int i = 0; i < 5; i++) nums.add(i * i);
        System.out.println(nums.get(3));     // 9
        nums.add(0, -1);                     // shifts every element
        System.out.println(nums);
    }
}
```
:::

:::callout{kind=tip}
If you know roughly how many elements you will store, reserve the capacity up front (`vector::reserve`, `new ArrayList<>(n)`) to skip the intermediate resizes.
:::

## Prefix sums: a first array technique

Many problems ask for the sum of a range `a[l..r]` many times. Recomputing it each time costs O(n) per query. Instead, precompute `prefix[i]`, the sum of the first `i` elements, once in O(n). Then every range sum is a single subtraction:

```python
def build_prefix(a):
    prefix = [0] * (len(a) + 1)
    for i, x in enumerate(a):
        prefix[i + 1] = prefix[i] + x
    return prefix

def range_sum(prefix, l, r):   # inclusive l..r
    return prefix[r + 1] - prefix[l]
```

## Where to go next

Arrays are the stage for most interview patterns. Learn [hash maps](/dsa/hash-maps-explained) to find things fast, then [two pointers](/dsa/two-pointers-technique) and [sliding windows](/dsa/sliding-window-technique) to scan arrays in a single pass.

::problem{slug=two-sum}
