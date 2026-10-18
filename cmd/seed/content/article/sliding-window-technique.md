A **sliding window** is a contiguous range `[left, right]` of an array or string that moves forward through the input. Instead of recomputing an answer for every possible range, which costs O(n²) or worse, you **update** it as one element enters on the right and another leaves on the left. Every element enters and leaves at most once, so the whole scan is O(n).

## Fixed-size windows

When the window length `k` is given, slide it one step at a time. Add the incoming element and subtract the outgoing one.

```python runnable title="Maximum sum of k consecutive elements"
def max_window_sum(a, k):
    window = sum(a[:k])
    best = window
    for right in range(k, len(a)):
        window += a[right] - a[right - k]   # one in, one out
        best = max(best, window)
    return best

print(max_window_sum([2, 1, 5, 1, 3, 2], 3))   # 9 (5 + 1 + 3)
```

## Variable-size windows

More often the window must satisfy a condition, such as "no repeated characters" or "sum at most S". The recipe:

1. Extend `right` one step and add `a[right]` to the window state.
2. While the window breaks the condition, remove `a[left]` and advance `left`.
3. The window is now valid, so record the answer.

### Longest substring without repeating characters

Track the last index where each character appeared. When `s[right]` was last seen inside the window, jump `left` just past it:

:::code-tabs
```python runnable title="Python" stdin="abcabcbb"
def longest_unique(s):
    last = {}
    left = best = 0
    for right, ch in enumerate(s):
        if ch in last and last[ch] >= left:
            left = last[ch] + 1
        last[ch] = right
        best = max(best, right - left + 1)
    return best

print(longest_unique(input().strip()))
```
```javascript runnable title="JavaScript"
function longestUnique(s) {
    const last = new Map();
    let left = 0, best = 0;
    for (let right = 0; right < s.length; right++) {
        const ch = s[right];
        if (last.has(ch) && last.get(ch) >= left) left = last.get(ch) + 1;
        last.set(ch, right);
        best = Math.max(best, right - left + 1);
    }
    return best;
}

console.log(longestUnique('pwwkew')); // 3
```
```java runnable title="Java"
import java.util.*;

public class Main {
    static int longestUnique(String s) {
        Map<Character, Integer> last = new HashMap<>();
        int left = 0, best = 0;
        for (int right = 0; right < s.length(); right++) {
            Integer seen = last.get(s.charAt(right));
            if (seen != null && seen >= left) left = seen + 1;
            last.put(s.charAt(right), right);
            best = Math.max(best, right - left + 1);
        }
        return best;
    }

    public static void main(String[] args) {
        System.out.println(longestUnique("abba")); // 2
    }
}
```
:::

:::callout{kind=warning}
The `last[ch] >= left` check matters. In `abba`, the final `a` was last seen at index 0, which is already outside the window `[2, 3]`. Jumping back to 1 would shrink the answer incorrectly.
:::

## When a window does not work

Shrinking from the left must never make a valid window invalid again. That holds for "at most k distinct", "no repeats" and "sum at most S" with **non-negative** numbers. With negative numbers, adding an element can lower the sum, so the window logic breaks. Use [prefix sums](/dsa/arrays-introduction) with a hash map, or [Kadane's algorithm](/dsa/kadanes-algorithm), instead.

## Checklist

- Is the answer about a **contiguous** subarray or substring?
- Can you update the window's state in O(1) when one element enters or leaves?
- Does the condition behave monotonically as the window shrinks?

If you can answer yes to all three, a sliding window is likely the intended O(n) solution.

::problem{slug=longest-substring-without-repeating-characters}
