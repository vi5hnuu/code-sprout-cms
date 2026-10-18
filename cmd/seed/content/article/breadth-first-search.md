**Breadth-first search** (BFS) explores a graph in layers. It visits the start node, then all nodes one edge away, then all nodes two edges away, and so on. Because it expands in order of distance, BFS finds **shortest paths in unweighted graphs**. The same idea counts connected regions in grids.

## Representing a graph

Most problems give edges as a list of pairs. Turn them into an **adjacency list**, which maps each node to its neighbours. It uses O(V + E) memory and lists a node's neighbours in time proportional to how many there are.

```python
from collections import defaultdict

def adjacency(edges):
    graph = defaultdict(list)
    for u, v in edges:
        graph[u].append(v)
        graph[v].append(u)   # omit for a directed graph
    return graph
```

An adjacency **matrix** (`V × V` booleans) answers "is there an edge between u and v?" in O(1), but costs O(V²) memory. It is only worth it for small, dense graphs.

## The algorithm

Keep a **queue** of nodes to visit and a set of nodes already seen. Mark a node as seen when you **enqueue** it, not when you dequeue it. Otherwise the same node can enter the queue many times.

:::code-tabs
```python runnable title="Python"
from collections import deque

def shortest_distances(graph, start):
    dist = {start: 0}
    queue = deque([start])
    while queue:
        node = queue.popleft()
        for nxt in graph[node]:
            if nxt not in dist:          # first visit = shortest distance
                dist[nxt] = dist[node] + 1
                queue.append(nxt)
    return dist

graph = {0: [1, 2], 1: [0, 3], 2: [0, 3], 3: [1, 2, 4], 4: [3]}
print(shortest_distances(graph, 0))   # {0: 0, 1: 1, 2: 1, 3: 2, 4: 3}
```
```java runnable title="Java"
import java.util.*;

public class Main {
    public static void main(String[] args) {
        int[][] graph = {{1, 2}, {0, 3}, {0, 3}, {1, 2, 4}, {3}};
        int[] dist = new int[graph.length];
        Arrays.fill(dist, -1);
        ArrayDeque<Integer> queue = new ArrayDeque<>();
        dist[0] = 0;
        queue.add(0);
        while (!queue.isEmpty()) {
            int node = queue.poll();
            for (int next : graph[node]) {
                if (dist[next] == -1) {
                    dist[next] = dist[node] + 1;
                    queue.add(next);
                }
            }
        }
        System.out.println(Arrays.toString(dist)); // [0, 1, 1, 2, 3]
    }
}
```
```cpp runnable title="C++"
#include <iostream>
#include <queue>
#include <vector>
using namespace std;

int main() {
    vector<vector<int>> graph = {{1, 2}, {0, 3}, {0, 3}, {1, 2, 4}, {3}};
    vector<int> dist(graph.size(), -1);
    queue<int> q;
    dist[0] = 0;
    q.push(0);
    while (!q.empty()) {
        int node = q.front(); q.pop();
        for (int next : graph[node])
            if (dist[next] == -1) { dist[next] = dist[node] + 1; q.push(next); }
    }
    for (int d : dist) cout << d << ' ';
    cout << '\n';
}
```
:::

Every node and edge is processed once, so BFS runs in **O(V + E)** time.

## Grids are graphs

In a grid, each cell is a node, and its up to four neighbours are the cells above, below, left and right. To **count islands** in a grid of land and water, start a BFS from every land cell you have not seen yet. Each BFS marks one whole island.

```python runnable title="Counting islands"
from collections import deque

def num_islands(grid):
    rows, cols = len(grid), len(grid[0])
    seen = set()
    count = 0
    for r in range(rows):
        for c in range(cols):
            if grid[r][c] != "1" or (r, c) in seen:
                continue
            count += 1
            seen.add((r, c))
            queue = deque([(r, c)])
            while queue:
                y, x = queue.popleft()
                for ny, nx in ((y + 1, x), (y - 1, x), (y, x + 1), (y, x - 1)):
                    if 0 <= ny < rows and 0 <= nx < cols and grid[ny][nx] == "1" and (ny, nx) not in seen:
                        seen.add((ny, nx))
                        queue.append((ny, nx))
    return count

print(num_islands(["11000", "11000", "00100", "00011"]))   # 3
```

:::callout{kind=info}
A recursive depth-first flood fill is shorter, but a 1,000 × 1,000 island means a million nested calls, which is far beyond the default stack of Python or Java. BFS with an explicit queue has no such limit.
:::

## BFS or DFS?

- **Shortest path, minimum number of moves, nearest X**: BFS.
- **Any path, cycle detection, topological order, exploring every configuration**: DFS is usually simpler.
- **Weighted edges**: neither. Use Dijkstra's algorithm, which is BFS with a priority queue.

::problem{slug=number-of-islands}
