The Java Collections Framework gives you interfaces (`List`, `Set`, `Map`, `Deque`) and several implementations of each. Choosing the right implementation is mostly about one question: **which operations must be fast?**

## Lists

| Implementation | get(i) | add at end | add/remove in middle | Notes |
|---|---|---|---|---|
| `ArrayList` | O(1) | O(1) amortised | O(n) | Default choice; cache-friendly |
| `LinkedList` | O(n) | O(1) | O(1) at an iterator | Rarely faster in practice |

`ArrayList` wins almost always. Even where `LinkedList` should be faster in theory, scattered nodes and pointer chasing usually make it slower than shifting a contiguous array.

## Sets and maps

| Implementation | Lookup | Ordered? | Notes |
|---|---|---|---|
| `HashMap` / `HashSet` | O(1) average | No | Default choice |
| `LinkedHashMap` / `LinkedHashSet` | O(1) | Insertion order | Predictable iteration; LRU caches |
| `TreeMap` / `TreeSet` | O(log n) | Sorted by key | `floorKey`, `ceilingKey`, ranges |

```java runnable title="Counting words with HashMap.merge"
import java.util.*;

public class Main {
    public static void main(String[] args) {
        String text = "the quick brown fox jumps over the lazy dog the end";
        Map<String, Integer> counts = new HashMap<>();
        for (String w : text.split(" ")) counts.merge(w, 1, Integer::sum);

        TreeMap<String, Integer> sorted = new TreeMap<>(counts);
        System.out.println(sorted);
        System.out.println("first word after 'm': " + sorted.ceilingKey("m"));
    }
}
```

:::callout{kind=warning}
Keys in a `HashMap` must implement `equals` and `hashCode` consistently. If two equal objects return different hash codes, lookups silently fail. Records generate both correctly for you.
:::

## Queues, stacks and deques

- Use **`ArrayDeque`** as both a stack (`push`/`pop`) and a queue (`offer`/`poll`). It is faster than the legacy `Stack` class and than `LinkedList`.
- Use **`PriorityQueue`** as a binary heap: `poll` returns the smallest element in O(log n). Pass a comparator for a max-heap or custom order.

```java runnable title="Top 3 largest with a min-heap"
import java.util.*;

public class Main {
    public static void main(String[] args) {
        int[] nums = {5, 1, 9, 3, 7, 6, 8};
        PriorityQueue<Integer> heap = new PriorityQueue<>();   // min-heap of size k
        for (int n : nums) {
            heap.offer(n);
            if (heap.size() > 3) heap.poll();                   // drop the smallest
        }
        List<Integer> top = new ArrayList<>(heap);
        top.sort(Comparator.reverseOrder());
        System.out.println(top);                                // [9, 8, 7]
    }
}
```

## Immutable collections

`List.of`, `Set.of` and `Map.of` (Java 9 and later) create compact, unmodifiable collections. Any modification throws `UnsupportedOperationException`. Use them for constants and for return values you do not want callers to change.

## Quick guide

- A sequence of items → `ArrayList`
- "Have I seen this?" → `HashSet`
- Key → value → `HashMap`, or `TreeMap` when you need sorted keys or ranges
- Stack or queue → `ArrayDeque`
- Repeatedly take the smallest or largest → `PriorityQueue`
