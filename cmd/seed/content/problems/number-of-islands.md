You are given a grid of `'1'` (land) and `'0'` (water). An **island** is a group of land cells connected horizontally or vertically. Count the islands.

Cells outside the grid are water.

## Input

- The first line holds `rows` and `cols`.
- Each of the next `rows` lines holds a string of `cols` characters, each `0` or `1`.

## Output

The number of islands.

## Constraints

- `1 ≤ rows, cols ≤ 300`

A recursive flood fill can overflow the call stack on a 300×300 island. An explicit queue, as in [Breadth-First Search on Graphs and Grids](/dsa/breadth-first-search), is safer.
