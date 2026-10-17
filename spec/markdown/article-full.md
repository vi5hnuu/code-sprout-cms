## Binary search

Binary search finds a target in a **sorted** array by halving the range each step.

![Halving the search range](asset:42)

:::callout[Why lo + (hi - lo) / 2?]{kind=tip}
Writing `(lo + hi) / 2` can overflow when both are large.
:::

### Implementation

:::code-tabs
```python runnable title="Python" stdin="5\n1 2 3 4 5"
def search(a, x):
    lo, hi = 0, len(a) - 1
```
```cpp runnable title="C++"
int search(vector<int>& a, int x);
```
```java title="Java"
int search(int[] a, int x) { return -1; }
```
:::

| Case | Time |
|------|------|
| Best | O(1) |
| Worst | O(log n) |

:::details[Show the recursive version]
Recursion trades the loop for the call stack.
:::

::problem{slug=search-in-rotated-array}

::video{file=77 title="Binary search walkthrough"}

See [complexity analysis](/dsa/complexity-analysis), the [CLRS book](https://mitpress.mit.edu/9780262046305/), or [mail us](mailto:hi@example.com).
