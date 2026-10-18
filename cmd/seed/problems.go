package main

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"

	"code-sprout-cms/internal/problem"
)

// Every sample problem reads stdin and writes stdout (the judge compares tokens), and ships
// starter code in these languages: an I/O harness around a function left for the reader.
var templateLanguages = []string{"python", "cpp", "java", "javascript"}

type testCase struct{ input, output string }

type problemSpec struct {
	slug, title, summary, category, author string
	tags                                   []string
	difficulty                             problem.Difficulty
	samples                                []problem.Sample
	hints                                  []string
	starter                                map[string]string
	// cases builds the hidden test set: fixed edge cases plus seeded random ones, answered by
	// a reference solution, so the set is identical on every run.
	cases func() []testCase
}

func rng(slug string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(slug))
	return rand.New(rand.NewPCG(h.Sum64(), 0x5eed))
}

func ptr[T any](v T) *T { return &v }

func ints(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, " ")
}

const randomCases = 25

var problemCatalog = []problemSpec{
	{
		slug: "two-sum", title: "Two Sum", author: "ada", category: "dsa/arrays", difficulty: problem.Easy,
		summary: "Find the two positions in an array whose values add up to a target, in a single pass with a hash map.",
		tags:    []string{"topic:arrays", "topic:hashing", "pattern:hash-map", "company:amazon", "company:google"},
		samples: []problem.Sample{
			{Input: "4\n2 7 11 15\n9\n", Output: "0 1\n", Explanation: ptr("nums[0] + nums[1] = 2 + 7 = 9.")},
			{Input: "3\n3 2 4\n6\n", Output: "1 2\n"},
		},
		hints: []string{
			"For each number x, the partner you need is target - x.",
			"Remember every value you have seen in a hash map from value to index; check for the partner before inserting x.",
		},
		starter: map[string]string{
			"python": `import sys


def two_sum(nums: list[int], target: int) -> tuple[int, int]:
    # Return (i, j) with i < j and nums[i] + nums[j] == target.
    pass


def main() -> None:
    data = sys.stdin.read().split()
    n = int(data[0])
    nums = list(map(int, data[1:1 + n]))
    target = int(data[1 + n])
    i, j = two_sum(nums, target)
    print(i, j)


main()
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

// Return {i, j} with i < j and nums[i] + nums[j] == target.
pair<int, int> twoSum(const vector<long long>& nums, long long target) {
    return {-1, -1};
}

int main() {
    ios::sync_with_stdio(false);
    cin.tie(nullptr);
    int n;
    cin >> n;
    vector<long long> nums(n);
    for (auto& x : nums) cin >> x;
    long long target;
    cin >> target;
    auto [i, j] = twoSum(nums, target);
    cout << i << ' ' << j << '\n';
}
`,
			"java": `import java.io.*;
import java.util.*;

public class Main {
    // Return {i, j} with i < j and nums[i] + nums[j] == target.
    static int[] twoSum(long[] nums, long target) {
        return new int[]{-1, -1};
    }

    public static void main(String[] args) throws IOException {
        StreamTokenizer in = new StreamTokenizer(new BufferedInputStream(System.in));
        in.nextToken();
        int n = (int) in.nval;
        long[] nums = new long[n];
        for (int i = 0; i < n; i++) { in.nextToken(); nums[i] = (long) in.nval; }
        in.nextToken();
        long target = (long) in.nval;
        int[] r = twoSum(nums, target);
        System.out.println(r[0] + " " + r[1]);
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

// Return [i, j] with i < j and nums[i] + nums[j] === target.
function twoSum(nums, target) {
    return [-1, -1];
}

const n = data[0];
const nums = data.slice(1, 1 + n);
const target = data[1 + n];
console.log(twoSum(nums, target).join(' '));
`,
		},
		cases: func() []testCase {
			r := rng("two-sum")
			out := []testCase{
				twoSumCase([]int{1, 2}, 0, 1),
				twoSumCase([]int{-3, 4, 3, 90}, 0, 2),
				twoSumCase([]int{5, 0, 0, 9}, 1, 2),
			}
			for len(out) < randomCases+3 {
				n := 2 + r.IntN(2000)
				seen := map[int]bool{}
				nums := make([]int, 0, n)
				for len(nums) < n {
					v := r.IntN(2_000_000_001) - 1_000_000_000
					if !seen[v] {
						seen[v] = true
						nums = append(nums, v)
					}
				}
				i := r.IntN(n - 1)
				j := i + 1 + r.IntN(n-i-1)
				if pairsSumming(nums, nums[i]+nums[j]) == 1 {
					out = append(out, twoSumCase(nums, i, j))
				}
			}
			return out
		},
	},
	{
		slug: "valid-parentheses", title: "Valid Parentheses", author: "ada", category: "dsa/arrays", difficulty: problem.Easy,
		summary: "Decide whether a string of brackets is balanced and properly nested, using a stack of open brackets.",
		tags:    []string{"topic:stack", "topic:strings", "pattern:stack", "company:meta"},
		samples: []problem.Sample{
			{Input: "()[]{}\n", Output: "true\n"},
			{Input: "([)]\n", Output: "false\n", Explanation: ptr("The ) closes a ( while [ is still open.")},
		},
		hints: []string{"Push every opening bracket; a closing bracket must match the top of the stack.", "The string is valid only if the stack ends empty."},
		starter: map[string]string{
			"python": `import sys


def is_valid(s: str) -> bool:
    pass


print("true" if is_valid(sys.stdin.readline().strip()) else "false")
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

bool isValid(const string& s) {
    return false;
}

int main() {
    string s;
    cin >> s;
    cout << (isValid(s) ? "true" : "false") << '\n';
}
`,
			"java": `import java.io.*;

public class Main {
    static boolean isValid(String s) {
        return false;
    }

    public static void main(String[] args) throws IOException {
        String s = new BufferedReader(new InputStreamReader(System.in)).readLine().trim();
        System.out.println(isValid(s) ? "true" : "false");
    }
}
`,
			"javascript": `const s = require('fs').readFileSync(0, 'utf8').trim();

function isValid(s) {
    return false;
}

console.log(isValid(s) ? 'true' : 'false');
`,
		},
		cases: func() []testCase {
			r := rng("valid-parentheses")
			out := []testCase{}
			for _, s := range []string{"(", ")", "()", "(]", "{[]}", "((((((", "]["} {
				out = append(out, testCase{s + "\n", boolWord(validBrackets(s)) + "\n"})
			}
			for range randomCases {
				s := balanced(r, 1+r.IntN(5000))
				if r.IntN(2) == 0 { // corrupt one position about half the time
					b := []byte(s)
					b[r.IntN(len(b))] = "()[]{}"[r.IntN(6)]
					s = string(b)
				}
				out = append(out, testCase{s + "\n", boolWord(validBrackets(s)) + "\n"})
			}
			return out
		},
	},
	{
		slug: "binary-search", title: "Binary Search", author: "grace", category: "dsa/searching", difficulty: problem.Easy,
		summary: "Locate a target in a sorted array of distinct integers in logarithmic time, or report that it is absent.",
		tags:    []string{"topic:binary-search", "topic:arrays", "pattern:binary-search"},
		samples: []problem.Sample{
			{Input: "6\n-1 0 3 5 9 12\n9\n", Output: "4\n"},
			{Input: "6\n-1 0 3 5 9 12\n2\n", Output: "-1\n", Explanation: ptr("2 is not in the array.")},
		},
		hints: []string{"Keep the invariant that the target, if present, lies in [lo, hi].", "Compute mid as lo + (hi - lo) / 2 to avoid overflow."},
		starter: map[string]string{
			"python": `import sys


def search(nums: list[int], target: int) -> int:
    pass


data = sys.stdin.read().split()
n = int(data[0])
print(search(list(map(int, data[1:1 + n])), int(data[1 + n])))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

int search(const vector<int>& nums, int target) {
    return -1;
}

int main() {
    ios::sync_with_stdio(false);
    cin.tie(nullptr);
    int n;
    cin >> n;
    vector<int> nums(n);
    for (auto& x : nums) cin >> x;
    int target;
    cin >> target;
    cout << search(nums, target) << '\n';
}
`,
			"java": `import java.io.*;

public class Main {
    static int search(int[] nums, int target) {
        return -1;
    }

    public static void main(String[] args) throws IOException {
        StreamTokenizer in = new StreamTokenizer(new BufferedInputStream(System.in));
        in.nextToken();
        int n = (int) in.nval;
        int[] nums = new int[n];
        for (int i = 0; i < n; i++) { in.nextToken(); nums[i] = (int) in.nval; }
        in.nextToken();
        System.out.println(search(nums, (int) in.nval));
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

function search(nums, target) {
    return -1;
}

const n = data[0];
console.log(search(data.slice(1, 1 + n), data[1 + n]));
`,
		},
		cases: func() []testCase {
			r := rng("binary-search")
			out := []testCase{bsCase([]int{5}, 5), bsCase([]int{5}, -5), bsCase([]int{1, 3}, 3), bsCase([]int{1, 3}, 0)}
			for range randomCases {
				n := 1 + r.IntN(5000)
				set := map[int]bool{}
				for len(set) < n {
					set[r.IntN(20001)-10000] = true
				}
				nums := make([]int, 0, n)
				for v := range set {
					nums = append(nums, v)
				}
				slices.Sort(nums)
				target := nums[r.IntN(n)]
				if r.IntN(3) == 0 {
					target = r.IntN(20001) - 10000
				}
				out = append(out, bsCase(nums, target))
			}
			return out
		},
	},
	{
		slug: "maximum-subarray", title: "Maximum Subarray", author: "grace", category: "dsa/dynamic-programming", difficulty: problem.Medium,
		summary: "Find the largest sum of any non-empty contiguous subarray with Kadane's linear-time algorithm.",
		tags:    []string{"topic:dynamic-programming", "topic:arrays", "pattern:kadane", "company:microsoft"},
		samples: []problem.Sample{
			{Input: "9\n-2 1 -3 4 -1 2 1 -5 4\n", Output: "6\n", Explanation: ptr("The subarray 4 -1 2 1 sums to 6.")},
			{Input: "1\n-7\n", Output: "-7\n"},
		},
		hints: []string{"The best subarray ending at i either extends the best one ending at i-1 or starts fresh at i.", "Track that running best and the overall best in one pass."},
		starter: map[string]string{
			"python": `import sys


def max_subarray(nums: list[int]) -> int:
    pass


data = sys.stdin.read().split()
n = int(data[0])
print(max_subarray(list(map(int, data[1:1 + n]))))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

long long maxSubarray(const vector<long long>& nums) {
    return 0;
}

int main() {
    ios::sync_with_stdio(false);
    cin.tie(nullptr);
    int n;
    cin >> n;
    vector<long long> nums(n);
    for (auto& x : nums) cin >> x;
    cout << maxSubarray(nums) << '\n';
}
`,
			"java": `import java.io.*;

public class Main {
    static long maxSubarray(long[] nums) {
        return 0;
    }

    public static void main(String[] args) throws IOException {
        StreamTokenizer in = new StreamTokenizer(new BufferedInputStream(System.in));
        in.nextToken();
        int n = (int) in.nval;
        long[] nums = new long[n];
        for (int i = 0; i < n; i++) { in.nextToken(); nums[i] = (long) in.nval; }
        System.out.println(maxSubarray(nums));
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

function maxSubarray(nums) {
    return 0;
}

console.log(maxSubarray(data.slice(1, 1 + data[0])));
`,
		},
		cases: func() []testCase {
			r := rng("maximum-subarray")
			out := []testCase{kadaneCase([]int{1}), kadaneCase([]int{-1, -2, -3}), kadaneCase([]int{5, -9, 6}), kadaneCase([]int{0, 0, 0})}
			for range randomCases {
				nums := make([]int, 1+r.IntN(100_000))
				for i := range nums {
					nums[i] = r.IntN(20001) - 10000
				}
				out = append(out, kadaneCase(nums))
			}
			return out
		},
	},
	{
		slug: "climbing-stairs", title: "Climbing Stairs", author: "grace", category: "dsa/dynamic-programming", difficulty: problem.Easy,
		summary: "Count the distinct ways to climb n steps taking one or two at a time, the gentlest introduction to dynamic programming.",
		tags:    []string{"topic:dynamic-programming", "topic:math", "pattern:fibonacci"},
		samples: []problem.Sample{
			{Input: "2\n", Output: "2\n", Explanation: ptr("1+1 or 2.")},
			{Input: "3\n", Output: "3\n", Explanation: ptr("1+1+1, 1+2 or 2+1.")},
		},
		hints: []string{"The last move was a single step or a double step.", "So ways(n) = ways(n-1) + ways(n-2); you only need the previous two values."},
		starter: map[string]string{
			"python": `def climb_stairs(n: int) -> int:
    pass


print(climb_stairs(int(input())))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

long long climbStairs(int n) {
    return 0;
}

int main() {
    int n;
    cin >> n;
    cout << climbStairs(n) << '\n';
}
`,
			"java": `import java.util.*;

public class Main {
    static long climbStairs(int n) {
        return 0;
    }

    public static void main(String[] args) {
        System.out.println(climbStairs(new Scanner(System.in).nextInt()));
    }
}
`,
			"javascript": `const n = Number(require('fs').readFileSync(0, 'utf8').trim());

function climbStairs(n) {
    return 0;
}

console.log(climbStairs(n));
`,
		},
		cases: func() []testCase {
			out := make([]testCase, 0, 45)
			a, b := 1, 1 // ways(0), ways(1)
			for n := 1; n <= 45; n++ {
				out = append(out, testCase{fmt.Sprintf("%d\n", n), fmt.Sprintf("%d\n", b)})
				a, b = b, a+b
			}
			return out
		},
	},
	{
		slug: "merge-intervals", title: "Merge Intervals", author: "linus", category: "dsa/sorting", difficulty: problem.Medium,
		summary: "Merge every overlapping interval in a list after sorting by start, a staple of calendar and range problems.",
		tags:    []string{"topic:sorting", "topic:arrays", "pattern:intervals", "company:google", "company:meta"},
		samples: []problem.Sample{
			{Input: "4\n1 3\n2 6\n8 10\n15 18\n", Output: "1 6\n8 10\n15 18\n", Explanation: ptr("[1,3] and [2,6] overlap, so they merge into [1,6].")},
			{Input: "2\n1 4\n4 5\n", Output: "1 5\n", Explanation: ptr("Intervals that touch are merged.")},
		},
		hints: []string{"Sort by start; then an interval overlaps the last merged one exactly when its start is at most that one's end."},
		starter: map[string]string{
			"python": `import sys


def merge(intervals: list[tuple[int, int]]) -> list[tuple[int, int]]:
    pass


data = list(map(int, sys.stdin.read().split()))
n = data[0]
pairs = [(data[1 + 2 * i], data[2 + 2 * i]) for i in range(n)]
print("\n".join(f"{a} {b}" for a, b in merge(pairs)))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

vector<pair<int, int>> mergeIntervals(vector<pair<int, int>> intervals) {
    return {};
}

int main() {
    ios::sync_with_stdio(false);
    cin.tie(nullptr);
    int n;
    cin >> n;
    vector<pair<int, int>> v(n);
    for (auto& [a, b] : v) cin >> a >> b;
    for (auto [a, b] : mergeIntervals(v)) cout << a << ' ' << b << '\n';
}
`,
			"java": `import java.io.*;
import java.util.*;

public class Main {
    static List<int[]> merge(int[][] intervals) {
        return new ArrayList<>();
    }

    public static void main(String[] args) throws IOException {
        StreamTokenizer in = new StreamTokenizer(new BufferedInputStream(System.in));
        in.nextToken();
        int n = (int) in.nval;
        int[][] v = new int[n][2];
        for (int i = 0; i < n; i++) {
            in.nextToken(); v[i][0] = (int) in.nval;
            in.nextToken(); v[i][1] = (int) in.nval;
        }
        StringBuilder sb = new StringBuilder();
        for (int[] p : merge(v)) sb.append(p[0]).append(' ').append(p[1]).append('\n');
        System.out.print(sb);
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

function merge(intervals) {
    return [];
}

const n = data[0];
const v = Array.from({ length: n }, (_, i) => [data[1 + 2 * i], data[2 + 2 * i]]);
console.log(merge(v).map(([a, b]) => a + ' ' + b).join('\n'));
`,
		},
		cases: func() []testCase {
			r := rng("merge-intervals")
			out := []testCase{mergeCase([][2]int{{1, 1}}), mergeCase([][2]int{{1, 10}, {2, 3}, {4, 5}}), mergeCase([][2]int{{5, 6}, {1, 2}})}
			for range randomCases {
				v := make([][2]int, 1+r.IntN(10_000))
				for i := range v {
					a := r.IntN(10_001)
					v[i] = [2]int{a, min(10_000, a+r.IntN(40))}
				}
				out = append(out, mergeCase(v))
			}
			return out
		},
	},
	{
		slug: "longest-substring-without-repeating-characters", title: "Longest Substring Without Repeating Characters", author: "linus", category: "dsa/arrays", difficulty: problem.Medium,
		summary: "Measure the longest stretch of a string with no repeated character using a sliding window over last-seen positions.",
		tags:    []string{"topic:strings", "topic:hashing", "pattern:sliding-window", "company:amazon"},
		samples: []problem.Sample{
			{Input: "abcabcbb\n", Output: "3\n", Explanation: ptr("\"abc\" is the longest such substring.")},
			{Input: "bbbbb\n", Output: "1\n"},
		},
		hints: []string{"Keep a window [left, i] with no repeats.", "When s[i] was last seen inside the window, move left just past that position."},
		starter: map[string]string{
			"python": `def length_of_longest_substring(s: str) -> int:
    pass


print(length_of_longest_substring(input().strip()))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

int lengthOfLongestSubstring(const string& s) {
    return 0;
}

int main() {
    string s;
    cin >> s;
    cout << lengthOfLongestSubstring(s) << '\n';
}
`,
			"java": `import java.io.*;

public class Main {
    static int lengthOfLongestSubstring(String s) {
        return 0;
    }

    public static void main(String[] args) throws IOException {
        String s = new BufferedReader(new InputStreamReader(System.in)).readLine().trim();
        System.out.println(lengthOfLongestSubstring(s));
    }
}
`,
			"javascript": `const s = require('fs').readFileSync(0, 'utf8').trim();

function lengthOfLongestSubstring(s) {
    return 0;
}

console.log(lengthOfLongestSubstring(s));
`,
		},
		cases: func() []testCase {
			r := rng("longest-substring")
			const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
			out := []testCase{}
			for _, s := range []string{"a", "pwwkew", "dvdf", "abba", alphabet} {
				out = append(out, testCase{s + "\n", fmt.Sprintf("%d\n", longestUnique(s))})
			}
			for range randomCases {
				k := 1 + r.IntN(len(alphabet))
				b := make([]byte, 1+r.IntN(50_000))
				for i := range b {
					b[i] = alphabet[r.IntN(k)]
				}
				out = append(out, testCase{string(b) + "\n", fmt.Sprintf("%d\n", longestUnique(string(b)))})
			}
			return out
		},
	},
	{
		slug: "number-of-islands", title: "Number of Islands", author: "linus", category: "dsa/graphs", difficulty: problem.Medium,
		summary: "Count the connected groups of land cells in a grid with breadth-first search, the gateway to graph traversal.",
		tags:    []string{"topic:graphs", "topic:matrix", "pattern:bfs", "company:amazon", "company:microsoft"},
		samples: []problem.Sample{
			{Input: "4 5\n11110\n11010\n11000\n00000\n", Output: "1\n"},
			{Input: "4 5\n11000\n11000\n00100\n00011\n", Output: "3\n", Explanation: ptr("Cells connect up, down, left and right, never diagonally.")},
		},
		hints: []string{"Each unvisited land cell starts a new island.", "Flood-fill from it (BFS or an explicit stack) so its whole island is marked visited."},
		starter: map[string]string{
			"python": `import sys


def num_islands(grid: list[str]) -> int:
    pass


data = sys.stdin.read().split()
rows = int(data[0])
print(num_islands(data[2:2 + rows]))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

int numIslands(vector<string> grid) {
    return 0;
}

int main() {
    int r, c;
    cin >> r >> c;
    vector<string> g(r);
    for (auto& row : g) cin >> row;
    cout << numIslands(g) << '\n';
}
`,
			"java": `import java.io.*;
import java.util.*;

public class Main {
    static int numIslands(char[][] grid) {
        return 0;
    }

    public static void main(String[] args) throws IOException {
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in));
        StringTokenizer st = new StringTokenizer(in.readLine());
        int r = Integer.parseInt(st.nextToken());
        char[][] g = new char[r][];
        for (int i = 0; i < r; i++) g[i] = in.readLine().trim().toCharArray();
        System.out.println(numIslands(g));
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/);

function numIslands(grid) {
    return 0;
}

const rows = Number(data[0]);
console.log(numIslands(data.slice(2, 2 + rows)));
`,
		},
		cases: func() []testCase {
			r := rng("number-of-islands")
			out := []testCase{islandsCase([]string{"0"}), islandsCase([]string{"1"}), islandsCase([]string{"101", "010", "101"})}
			for range randomCases {
				rows, cols := 1+r.IntN(200), 1+r.IntN(200)
				density := 0.2 + r.Float64()*0.5
				grid := make([]string, rows)
				for i := range grid {
					b := make([]byte, cols)
					for j := range b {
						b[j] = '0'
						if r.Float64() < density {
							b[j] = '1'
						}
					}
					grid[i] = string(b)
				}
				out = append(out, islandsCase(grid))
			}
			return out
		},
	},
	{
		slug: "top-k-frequent-elements", title: "Top K Frequent Elements", author: "ada", category: "dsa/sorting", difficulty: problem.Medium,
		summary: "Return the k values that occur most often, with bucket sort by frequency for a linear-time answer.",
		tags:    []string{"topic:hashing", "topic:sorting", "pattern:bucket-sort", "company:amazon"},
		samples: []problem.Sample{
			{Input: "6 2\n1 1 1 2 2 3\n", Output: "1 2\n"},
			{Input: "5 2\n4 4 6 6 5\n", Output: "4 6\n", Explanation: ptr("4 and 6 both occur twice; ties are listed smaller value first.")},
		},
		hints: []string{"Count occurrences with a hash map.", "Order by count (highest first), breaking ties by the smaller value, and take the first k."},
		starter: map[string]string{
			"python": `import sys


def top_k_frequent(nums: list[int], k: int) -> list[int]:
    # Highest count first; equal counts in increasing value order.
    pass


data = list(map(int, sys.stdin.read().split()))
n, k = data[0], data[1]
print(*top_k_frequent(data[2:2 + n], k))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

// Highest count first; equal counts in increasing value order.
vector<int> topKFrequent(const vector<int>& nums, int k) {
    return {};
}

int main() {
    ios::sync_with_stdio(false);
    cin.tie(nullptr);
    int n, k;
    cin >> n >> k;
    vector<int> nums(n);
    for (auto& x : nums) cin >> x;
    auto r = topKFrequent(nums, k);
    for (size_t i = 0; i < r.size(); i++) cout << r[i] << (i + 1 < r.size() ? ' ' : '\n');
}
`,
			"java": `import java.io.*;
import java.util.*;

public class Main {
    // Highest count first; equal counts in increasing value order.
    static int[] topKFrequent(int[] nums, int k) {
        return new int[0];
    }

    public static void main(String[] args) throws IOException {
        StreamTokenizer in = new StreamTokenizer(new BufferedInputStream(System.in));
        in.nextToken();
        int n = (int) in.nval;
        in.nextToken();
        int k = (int) in.nval;
        int[] nums = new int[n];
        for (int i = 0; i < n; i++) { in.nextToken(); nums[i] = (int) in.nval; }
        StringJoiner sj = new StringJoiner(" ");
        for (int x : topKFrequent(nums, k)) sj.add(String.valueOf(x));
        System.out.println(sj);
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

// Highest count first; equal counts in increasing value order.
function topKFrequent(nums, k) {
    return [];
}

const [n, k] = data;
console.log(topKFrequent(data.slice(2, 2 + n), k).join(' '));
`,
		},
		cases: func() []testCase {
			r := rng("top-k-frequent")
			out := []testCase{topKCase([]int{7}, 1), topKCase([]int{-1, -1, 2}, 2)}
			for range randomCases {
				spread := 1 + r.IntN(2000)
				nums := make([]int, 1+r.IntN(10_000))
				for i := range nums {
					nums[i] = r.IntN(spread) - spread/2
				}
				distinct := len(counts(nums))
				out = append(out, topKCase(nums, 1+r.IntN(distinct)))
			}
			return out
		},
	},
	{
		slug: "coin-change", title: "Coin Change", author: "grace", category: "dsa/dynamic-programming", difficulty: problem.Medium,
		summary: "Find the fewest coins that make up an amount with bottom-up dynamic programming, or report that it cannot be made.",
		tags:    []string{"topic:dynamic-programming", "pattern:unbounded-knapsack", "company:google"},
		samples: []problem.Sample{
			{Input: "3 11\n1 2 5\n", Output: "3\n", Explanation: ptr("11 = 5 + 5 + 1.")},
			{Input: "1 3\n2\n", Output: "-1\n"},
			{Input: "1 0\n1\n", Output: "0\n"},
		},
		hints: []string{"Let best[a] be the fewest coins for amount a; best[0] = 0.", "best[a] = 1 + min(best[a - c]) over coins c ≤ a, filled from 1 up to the amount."},
		starter: map[string]string{
			"python": `import sys


def coin_change(coins: list[int], amount: int) -> int:
    pass


data = list(map(int, sys.stdin.read().split()))
n, amount = data[0], data[1]
print(coin_change(data[2:2 + n], amount))
`,
			"cpp": `#include <bits/stdc++.h>
using namespace std;

int coinChange(const vector<int>& coins, int amount) {
    return -1;
}

int main() {
    int n, amount;
    cin >> n >> amount;
    vector<int> coins(n);
    for (auto& c : coins) cin >> c;
    cout << coinChange(coins, amount) << '\n';
}
`,
			"java": `import java.util.*;

public class Main {
    static int coinChange(int[] coins, int amount) {
        return -1;
    }

    public static void main(String[] args) {
        Scanner in = new Scanner(System.in);
        int n = in.nextInt(), amount = in.nextInt();
        int[] coins = new int[n];
        for (int i = 0; i < n; i++) coins[i] = in.nextInt();
        System.out.println(coinChange(coins, amount));
    }
}
`,
			"javascript": `const data = require('fs').readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);

function coinChange(coins, amount) {
    return -1;
}

const [n, amount] = data;
console.log(coinChange(data.slice(2, 2 + n), amount));
`,
		},
		cases: func() []testCase {
			r := rng("coin-change")
			out := []testCase{coinCase([]int{1}, 1), coinCase([]int{3, 7}, 5), coinCase([]int{186, 419, 83, 408}, 6249)}
			for range randomCases {
				coins := make([]int, 1+r.IntN(12))
				for i := range coins {
					coins[i] = 1 + r.IntN(500)
				}
				out = append(out, coinCase(coins, r.IntN(10_001)))
			}
			return out
		},
	},
}

// ── Reference solutions and case builders ────────────────────────────────────

func twoSumCase(nums []int, i, j int) testCase {
	return testCase{fmt.Sprintf("%d\n%s\n%d\n", len(nums), ints(nums), nums[i]+nums[j]), fmt.Sprintf("%d %d\n", i, j)}
}

// pairsSumming counts index pairs with the given sum, so generated cases have one answer.
func pairsSumming(nums []int, target int) int {
	seen := map[int]int{}
	n := 0
	for _, x := range nums {
		n += seen[target-x]
		seen[x]++
	}
	return n
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func validBrackets(s string) bool {
	pair := map[byte]byte{')': '(', ']': '[', '}': '{'}
	var stack []byte
	for i := range len(s) {
		c := s[i]
		if open, closing := pair[c]; closing {
			if len(stack) == 0 || stack[len(stack)-1] != open {
				return false
			}
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, c)
		}
	}
	return len(stack) == 0
}

// balanced builds a random well-nested bracket string of about n characters.
func balanced(r *rand.Rand, n int) string {
	var b strings.Builder
	var stack []byte
	closeOf := map[byte]byte{'(': ')', '[': ']', '{': '}'}
	for b.Len()+len(stack) < n || len(stack) > 0 {
		if len(stack) > 0 && (b.Len()+len(stack) >= n || r.IntN(2) == 0) {
			b.WriteByte(closeOf[stack[len(stack)-1]])
			stack = stack[:len(stack)-1]
			continue
		}
		o := "([{"[r.IntN(3)]
		b.WriteByte(o)
		stack = append(stack, o)
	}
	return b.String()
}

func bsCase(nums []int, target int) testCase {
	idx := sort.SearchInts(nums, target)
	if idx == len(nums) || nums[idx] != target {
		idx = -1
	}
	return testCase{fmt.Sprintf("%d\n%s\n%d\n", len(nums), ints(nums), target), fmt.Sprintf("%d\n", idx)}
}

func kadaneCase(nums []int) testCase {
	best, cur := nums[0], nums[0]
	for _, x := range nums[1:] {
		cur = max(x, cur+x)
		best = max(best, cur)
	}
	return testCase{fmt.Sprintf("%d\n%s\n", len(nums), ints(nums)), fmt.Sprintf("%d\n", best)}
}

func mergeCase(v [][2]int) testCase {
	var in strings.Builder
	fmt.Fprintf(&in, "%d\n", len(v))
	for _, p := range v {
		fmt.Fprintf(&in, "%d %d\n", p[0], p[1])
	}
	s := slices.Clone(v)
	slices.SortFunc(s, func(a, b [2]int) int { return a[0] - b[0] })
	var merged [][2]int
	for _, p := range s {
		if n := len(merged); n > 0 && p[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], p[1])
		} else {
			merged = append(merged, p)
		}
	}
	var out strings.Builder
	for _, p := range merged {
		fmt.Fprintf(&out, "%d %d\n", p[0], p[1])
	}
	return testCase{in.String(), out.String()}
}

func longestUnique(s string) int {
	last := map[byte]int{}
	best, left := 0, 0
	for i := range len(s) {
		if j, ok := last[s[i]]; ok && j >= left {
			left = j + 1
		}
		last[s[i]] = i
		best = max(best, i-left+1)
	}
	return best
}

func islandsCase(grid []string) testCase {
	rows, cols := len(grid), len(grid[0])
	seen := make([]bool, rows*cols)
	count := 0
	for i := range rows {
		for j := range cols {
			if grid[i][j] != '1' || seen[i*cols+j] {
				continue
			}
			count++
			queue := []int{i*cols + j}
			seen[i*cols+j] = true
			for len(queue) > 0 {
				cell := queue[0]
				queue = queue[1:]
				r, c := cell/cols, cell%cols
				for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nr, nc := r+d[0], c+d[1]
					if nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] == '1' && !seen[nr*cols+nc] {
						seen[nr*cols+nc] = true
						queue = append(queue, nr*cols+nc)
					}
				}
			}
		}
	}
	return testCase{fmt.Sprintf("%d %d\n%s\n", rows, cols, strings.Join(grid, "\n")), fmt.Sprintf("%d\n", count)}
}

func counts(nums []int) map[int]int {
	m := map[int]int{}
	for _, x := range nums {
		m[x]++
	}
	return m
}

func topKCase(nums []int, k int) testCase {
	m := counts(nums)
	vals := make([]int, 0, len(m))
	for v := range m {
		vals = append(vals, v)
	}
	slices.SortFunc(vals, func(a, b int) int {
		if m[a] != m[b] {
			return m[b] - m[a]
		}
		return a - b
	})
	return testCase{fmt.Sprintf("%d %d\n%s\n", len(nums), k, ints(nums)), ints(vals[:k]) + "\n"}
}

func coinCase(coins []int, amount int) testCase {
	const inf = 1 << 30
	best := make([]int, amount+1)
	for a := 1; a <= amount; a++ {
		best[a] = inf
		for _, c := range coins {
			if c <= a && best[a-c]+1 < best[a] {
				best[a] = best[a-c] + 1
			}
		}
	}
	ans := best[amount]
	if ans >= inf {
		ans = -1
	}
	return testCase{fmt.Sprintf("%d %d\n%s\n", len(coins), amount, ints(coins)), fmt.Sprintf("%d\n", ans)}
}
