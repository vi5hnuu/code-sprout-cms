A string made only of the characters `(`, `)`, `[`, `]`, `{` and `}` is **valid** when:

1. every opening bracket is closed by a bracket of the same type, and
2. brackets close in the reverse order they were opened.

Decide whether the given string is valid.

## Input

A single line containing the string `s`.

## Output

Print `true` if `s` is valid, otherwise `false`.

## Constraints

- `1 ≤ |s| ≤ 10^4`
- `s` contains only `()[]{}`.
