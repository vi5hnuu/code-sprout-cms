A **hash map** (dictionary, `HashMap`, `unordered_map`, `Map`) stores key–value pairs and finds any key in **O(1) time on average**. It is the single most useful tool for turning slow nested loops into one pass.

## How it works

A hash map is an array of *buckets*. To store a key, it:

1. computes a **hash**: an integer derived from the key, such as a mix of a string's characters;
2. reduces the hash to a bucket index, for example `hash % capacity`;
3. stores the pair in that bucket.

Looking a key up repeats the same two steps and checks one bucket. That bucket rarely holds more than a couple of entries, so the cost does not depend on how many keys the map holds.

## Collisions

Different keys can land in the same bucket. Implementations handle this in one of two ways:

- **Separate chaining**: each bucket holds a small list (Java's `HashMap` upgrades long chains to balanced trees).
- **Open addressing**: on a collision, probe the next free slot (Python's `dict`, many C++ libraries).

When the map gets too full, its *load factor* passes a threshold (0.75 in Java). It then doubles its bucket array and rehashes every key. Like dynamic arrays, that keeps the **amortised** cost per insert constant.

:::callout{kind=warning}
"O(1)" is an average. A poor hash function, or input crafted to collide, can make every key land in one bucket and lookups O(n). Standard-library hashes are randomised or tree-backed to defend against this.
:::

## The pattern: remember what you have seen

The classic use is **Two Sum**: find two numbers in an array that add up to a target. Checking every pair costs O(n²). Instead, walk the array once and, for each value `x`, ask the map whether `target - x` has already appeared:

:::code-tabs
```python runnable title="Python" stdin="4\n2 7 11 15\n9"
def two_sum(nums, target):
    seen = {}                       # value -> index
    for i, x in enumerate(nums):
        if target - x in seen:
            return seen[target - x], i
        seen[x] = i

n = int(input())
nums = list(map(int, input().split()))
print(*two_sum(nums, int(input())))
```
```javascript runnable title="JavaScript"
function twoSum(nums, target) {
    const seen = new Map();          // value -> index
    for (let i = 0; i < nums.length; i++) {
        const need = target - nums[i];
        if (seen.has(need)) return [seen.get(need), i];
        seen.set(nums[i], i);
    }
    return [];
}

console.log(twoSum([2, 7, 11, 15], 9)); // [ 0, 1 ]
```
```java runnable title="Java"
import java.util.*;

public class Main {
    static int[] twoSum(int[] nums, int target) {
        Map<Integer, Integer> seen = new HashMap<>();
        for (int i = 0; i < nums.length; i++) {
            Integer j = seen.get(target - nums[i]);
            if (j != null) return new int[]{j, i};
            seen.put(nums[i], i);
        }
        return new int[0];
    }

    public static void main(String[] args) {
        System.out.println(Arrays.toString(twoSum(new int[]{2, 7, 11, 15}, 9)));
    }
}
```
:::

The check happens *before* the insert, so an element is never paired with itself.

## Counting with a map

The second everyday pattern is counting: frequencies of words, characters or numbers.

```python
from collections import Counter

counts = Counter("mississippi")
print(counts.most_common(2))   # [('i', 4), ('s', 4)]
```

Counting is the first step of problems such as anagram grouping and *top k frequent elements*.

## When not to use a hash map

- You need keys **in order** (smallest, next larger, a range): use a balanced tree (`TreeMap`, `std::map`) or a sorted array.
- Keys are small integers in a known range: a plain array indexed by the key is faster and smaller.

::problem{slug=two-sum}

::problem{slug=top-k-frequent-elements}
