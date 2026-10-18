Comprehensions build a list, dict or set from an iterable in one readable expression. **Generator expressions** do the same lazily, one item at a time, without building the collection in memory. Used well, they make Python code shorter and faster. Used for everything, they make it harder to read.

## List comprehensions

```python runnable
squares = [n * n for n in range(10)]
evens = [n for n in range(20) if n % 2 == 0]
words = ["apple", "Banana", "cherry"]
lowered = [w.lower() for w in words]

print(squares)
print(evens)
print(lowered)
```

The pattern is `[expression for item in iterable if condition]`. It is equivalent to a loop that appends, but clearer, and usually a little faster because the append is done in C.

## Dict and set comprehensions

```python runnable
words = ["apple", "banana", "cherry", "avocado"]

lengths = {w: len(w) for w in words}
initials = {w[0] for w in words}
by_letter = {}
for w in words:                       # grouping reads better as a loop
    by_letter.setdefault(w[0], []).append(w)

print(lengths)
print(initials)
print(by_letter)
```

## Nested loops

Clauses read left to right, in the same order as nested `for` statements:

```python runnable
pairs = [(x, y) for x in range(3) for y in range(3) if x != y]
matrix = [[1, 2, 3], [4, 5, 6]]
flat = [v for row in matrix for v in row]
transposed = [[row[i] for row in matrix] for i in range(3)]

print(pairs)
print(flat)
print(transposed)
```

:::callout{kind=warning}
Two `for` clauses are about the limit. Beyond that, or with side effects, write a normal loop. Comprehensions are for building values, not for running actions.
:::

## Generator expressions: lazy and memory-light

Replace the brackets with parentheses and nothing is computed until you iterate:

```python runnable
import sys

eager = [n * n for n in range(1_000_000)]
lazy = (n * n for n in range(1_000_000))

print(sys.getsizeof(eager), "bytes for the list")
print(sys.getsizeof(lazy), "bytes for the generator")
print(sum(n * n for n in range(1_000_000)))   # no intermediate list at all
```

Use generators when you only need to iterate once: for summing, finding the first match with `next(...)`, or streaming lines from a large file.

```python
with open("access.log") as f:
    errors = sum(1 for line in f if " 500 " in line)
```

## The walrus operator in comprehensions

`:=` lets you compute a value once and both filter on it and keep it:

```python
results = [y for x in data if (y := expensive(x)) is not None]
```

## Rules of thumb

- **Comprehension** when you are building a new list, dict or set from an existing iterable.
- **Generator expression** when you consume the values once, especially inside `sum`, `any`, `all`, `max` or `"".join`.
- **A loop** when there are side effects, complex branching or more than two levels of nesting.
