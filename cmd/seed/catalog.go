package main

import (
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/taxonomy"
)

// The sample catalog. Bodies live in content/<type>/<slug>.md; everything else is here.

type authorSpec struct{ key, name, handle, bio string }

var authors = []authorSpec{
	{"ada", "Ada Raman", "ada-raman", "Competitive programmer turned teacher. Writes about arrays, hashing and the interview patterns that keep coming back."},
	{"grace", "Grace Okafor", "grace-okafor", "Algorithms engineer. Believes every dynamic programming problem is a recurrence waiting to be written down."},
	{"linus", "Linus Bergström", "linus-bergstrom", "Backend developer working on search and graph workloads. Likes BFS more than he should."},
	{"meera", "Meera Iyer", "meera-iyer", "Front-end and platform engineer. Writes about JavaScript internals, CSS layout and systems that stay fast at scale."},
}

type categorySpec struct{ path, name, description string }

// Parents come before their children.
var categories = []categorySpec{
	{"dsa", "Data Structures & Algorithms", "Arrays to graphs: the core structures, the algorithms on them and the patterns interviews test."},
	{"dsa/arrays", "Arrays & Hashing", "Contiguous storage, two pointers, sliding windows and hash maps."},
	{"dsa/searching", "Searching", "Binary search and its many disguises."},
	{"dsa/sorting", "Sorting", "Comparison sorts, their trade-offs and problems that become easy once sorted."},
	{"dsa/dynamic-programming", "Dynamic Programming", "Breaking problems into overlapping subproblems and reusing their answers."},
	{"dsa/graphs", "Graphs", "Representations, traversals and shortest paths."},
	{"web", "Web Development", "JavaScript, React and CSS, from fundamentals to how the browser runs your code."},
	{"web/javascript", "JavaScript", "The language of the web: scope, closures, the event loop and more."},
	{"web/react", "React", "Components, state and rendering."},
	{"web/css", "CSS", "Layout, typography and responsive design."},
	{"system-design", "System Design", "Building services that stay fast and correct as traffic grows."},
	{"system-design/fundamentals", "Fundamentals", "Latency, throughput, caching and the trade-offs behind them."},
	{"system-design/databases", "Databases", "Indexes, transactions and scaling reads and writes."},
	{"languages", "Programming Languages", "Idioms and standard libraries of popular languages."},
	{"languages/python", "Python", "Readable, batteries-included Python."},
	{"languages/java", "Java", "Modern Java and its collections."},
	{"guides", "Guides", "How Code Sprout works, and how to write for it."},
}

type tagSpec struct {
	kind       taxonomy.TagKind
	slug, name string
}

var tags = []tagSpec{
	{taxonomy.TagTopic, "arrays", "Arrays"},
	{taxonomy.TagTopic, "strings", "Strings"},
	{taxonomy.TagTopic, "hashing", "Hashing"},
	{taxonomy.TagTopic, "stack", "Stack"},
	{taxonomy.TagTopic, "binary-search", "Binary Search"},
	{taxonomy.TagTopic, "sorting", "Sorting"},
	{taxonomy.TagTopic, "dynamic-programming", "Dynamic Programming"},
	{taxonomy.TagTopic, "graphs", "Graphs"},
	{taxonomy.TagTopic, "matrix", "Matrix"},
	{taxonomy.TagTopic, "math", "Math"},
	{taxonomy.TagTopic, "recursion", "Recursion"},
	{taxonomy.TagTopic, "caching", "Caching"},
	{taxonomy.TagTopic, "databases", "Databases"},
	{taxonomy.TagTopic, "interviews", "Interviews"},
	{taxonomy.TagPattern, "hash-map", "Hash Map"},
	{taxonomy.TagPattern, "two-pointers", "Two Pointers"},
	{taxonomy.TagPattern, "sliding-window", "Sliding Window"},
	{taxonomy.TagPattern, "binary-search", "Binary Search"},
	{taxonomy.TagPattern, "kadane", "Kadane's Algorithm"},
	{taxonomy.TagPattern, "fibonacci", "Fibonacci-style DP"},
	{taxonomy.TagPattern, "intervals", "Intervals"},
	{taxonomy.TagPattern, "bfs", "Breadth-First Search"},
	{taxonomy.TagPattern, "bucket-sort", "Bucket Sort"},
	{taxonomy.TagPattern, "unbounded-knapsack", "Unbounded Knapsack"},
	{taxonomy.TagPattern, "stack", "Monotonic & Matching Stack"},
	{taxonomy.TagCompany, "amazon", "Amazon"},
	{taxonomy.TagCompany, "google", "Google"},
	{taxonomy.TagCompany, "meta", "Meta"},
	{taxonomy.TagCompany, "microsoft", "Microsoft"},
	{taxonomy.TagLanguage, "python", "Python"},
	{taxonomy.TagLanguage, "java", "Java"},
	{taxonomy.TagLanguage, "javascript", "JavaScript"},
	{taxonomy.TagLanguage, "cpp", "C++"},
	{taxonomy.TagLanguage, "css", "CSS"},
}

type pageSpec struct {
	typ                                    content.Type
	slug, title, summary, category, author string
	tags                                   []string
	level                                  string  // articles: BEGINNER | INTERMEDIATE | ADVANCED
	coverHue                               float64 // blogs: base hue of the generated cover
}

const (
	beginner     = "BEGINNER"
	intermediate = "INTERMEDIATE"
	advanced     = "ADVANCED"
)

var pageCatalog = []pageSpec{
	// ── DSA ──
	{typ: content.TypeArticle, slug: "arrays-introduction", title: "Arrays: How They Work and What They Cost", author: "ada", category: "dsa/arrays", level: beginner,
		summary: "How arrays sit in memory, why indexing is O(1) while inserting in the middle is O(n), and how dynamic arrays grow.",
		tags:    []string{"topic:arrays", "language:python", "language:cpp"}},
	{typ: content.TypeArticle, slug: "hash-maps-explained", title: "Hash Maps Explained: Constant-Time Lookups", author: "ada", category: "dsa/arrays", level: beginner,
		summary: "What a hash function does, how collisions are handled, and the hash-map pattern that turns O(n²) searches into one pass.",
		tags:    []string{"topic:hashing", "pattern:hash-map", "topic:interviews"}},
	{typ: content.TypeArticle, slug: "two-pointers-technique", title: "The Two Pointers Technique", author: "ada", category: "dsa/arrays", level: intermediate,
		summary: "Walk two indices through an array to solve pair-sum, partition and palindrome problems in linear time and constant space.",
		tags:    []string{"topic:arrays", "pattern:two-pointers", "topic:interviews"}},
	{typ: content.TypeArticle, slug: "sliding-window-technique", title: "Sliding Window: Fixed and Variable Windows", author: "linus", category: "dsa/arrays", level: intermediate,
		summary: "Maintain a window over a sequence and update it incrementally to answer subarray and substring questions in O(n).",
		tags:    []string{"topic:strings", "pattern:sliding-window", "topic:hashing"}},
	{typ: content.TypeArticle, slug: "binary-search", title: "Binary Search Without Off-by-One Errors", author: "grace", category: "dsa/searching", level: beginner,
		summary: "A precise invariant for binary search, the lower-bound variant, and how to avoid the classic overflow and infinite-loop bugs.",
		tags:    []string{"topic:binary-search", "pattern:binary-search", "language:java"}},
	{typ: content.TypeArticle, slug: "binary-search-on-the-answer", title: "Binary Search on the Answer", author: "grace", category: "dsa/searching", level: advanced,
		summary: "When a yes/no check is monotonic in a number, binary search that number: shipping capacities, minimum speeds and more.",
		tags:    []string{"topic:binary-search", "pattern:binary-search", "topic:interviews"}},
	{typ: content.TypeArticle, slug: "sorting-algorithms-compared", title: "Sorting Algorithms Compared", author: "linus", category: "dsa/sorting", level: intermediate,
		summary: "Insertion, merge, quick and counting sort side by side: time, space, stability, and which one your standard library uses.",
		tags:    []string{"topic:sorting", "pattern:intervals", "language:cpp"}},
	{typ: content.TypeArticle, slug: "introduction-to-dynamic-programming", title: "Introduction to Dynamic Programming", author: "grace", category: "dsa/dynamic-programming", level: beginner,
		summary: "From naive recursion to memoisation to a bottom-up table: the three-step recipe for turning a recurrence into fast code.",
		tags:    []string{"topic:dynamic-programming", "topic:recursion", "pattern:fibonacci"}},
	{typ: content.TypeArticle, slug: "kadanes-algorithm", title: "Kadane's Algorithm for Maximum Subarray Sum", author: "grace", category: "dsa/dynamic-programming", level: intermediate,
		summary: "Why the best subarray ending here either extends the previous one or starts fresh, and how that gives an O(n) solution.",
		tags:    []string{"topic:dynamic-programming", "pattern:kadane", "topic:arrays"}},
	{typ: content.TypeArticle, slug: "breadth-first-search", title: "Breadth-First Search on Graphs and Grids", author: "linus", category: "dsa/graphs", level: intermediate,
		summary: "Adjacency lists, the BFS queue, shortest paths in unweighted graphs, and flood fill on grids, with code in four languages.",
		tags:    []string{"topic:graphs", "pattern:bfs", "topic:matrix"}},
	{typ: content.TypeTopicPage, slug: "dsa-roadmap", title: "The DSA Roadmap", author: "ada", category: "dsa",
		summary: "A suggested order for learning data structures and algorithms, with the articles and practice problems for each stage.",
		tags:    []string{"topic:interviews", "topic:arrays", "topic:dynamic-programming"}},

	// ── Web ──
	{typ: content.TypeArticle, slug: "javascript-closures", title: "JavaScript Closures, Finally Explained", author: "meera", category: "web/javascript", level: beginner,
		summary: "What a closure captures, why loop variables surprised everyone before let, and practical uses from counters to memoisation.",
		tags:    []string{"language:javascript"}},
	{typ: content.TypeArticle, slug: "javascript-event-loop", title: "The JavaScript Event Loop", author: "meera", category: "web/javascript", level: intermediate,
		summary: "Call stack, task queue and microtasks: why a resolved promise runs before a zero-delay timeout and how to keep the UI responsive.",
		tags:    []string{"language:javascript"}},
	{typ: content.TypeArticle, slug: "react-state-and-rendering", title: "React State and Rendering", author: "meera", category: "web/react", level: intermediate,
		summary: "When React re-renders, why state updates are snapshots, and how keys and derived state prevent the most common bugs.",
		tags:    []string{"language:javascript"}},
	{typ: content.TypeArticle, slug: "css-grid-vs-flexbox", title: "CSS Grid vs Flexbox: When to Use Which", author: "meera", category: "web/css", level: beginner,
		summary: "Flexbox lays out along one axis, Grid along two. Concrete layouts that show which tool fits, and how they combine.",
		tags:    []string{"language:css"}},

	// ── System design ──
	{typ: content.TypeArticle, slug: "caching-strategies", title: "Caching Strategies: Cache-Aside, Write-Through and Beyond", author: "meera", category: "system-design/fundamentals", level: intermediate,
		summary: "Where to put a cache, how to keep it fresh, and how to avoid stampedes when a hot key expires under load.",
		tags:    []string{"topic:caching", "topic:interviews"}},
	{typ: content.TypeArticle, slug: "database-indexing", title: "Database Indexing: How B-Trees Make Queries Fast", author: "linus", category: "system-design/databases", level: intermediate,
		summary: "How a B-tree index turns a full table scan into a few page reads, composite index order, and why OFFSET pagination slows down.",
		tags:    []string{"topic:databases", "topic:interviews"}},

	// ── Languages ──
	{typ: content.TypeArticle, slug: "python-comprehensions", title: "Python Comprehensions and Generators", author: "ada", category: "languages/python", level: beginner,
		summary: "List, dict and set comprehensions, generator expressions, and when a plain loop is the clearer choice.",
		tags:    []string{"language:python"}},
	{typ: content.TypeArticle, slug: "java-collections", title: "A Tour of the Java Collections Framework", author: "linus", category: "languages/java", level: beginner,
		summary: "List, Set, Map and Deque: the main implementations, their complexity, and which one to reach for in interviews and production.",
		tags:    []string{"language:java", "topic:hashing"}},

	// ── Guides ──
	{typ: content.TypeTopicPage, slug: "writing-guide", title: "Writing for Code Sprout", author: "ada", category: "guides",
		summary: "How to write a great Code Sprout article: structure, runnable examples, callouts, embedding practice problems and review.",
		tags:    []string{}},

	// ── Blog ──
	{typ: content.TypeBlog, slug: "welcome-to-code-sprout", title: "Welcome to the New Code Sprout", author: "ada", category: "guides", coverHue: 145,
		summary: "Tutorials with runnable code, problems judged in real sandboxes and study plans that track your progress: here is what is new.",
		tags:    []string{}},
	{typ: content.TypeBlog, slug: "how-we-judge-your-code", title: "How We Judge Your Code", author: "linus", category: "system-design/fundamentals", coverHue: 210,
		summary: "Every submission runs in its own microVM with strict time and memory limits. A look at the judge behind the Submit button.",
		tags:    []string{"topic:caching"}},
	{typ: content.TypeBlog, slug: "interview-prep-plan", title: "A Realistic 8-Week Interview Prep Plan", author: "grace", category: "dsa", coverHue: 28,
		summary: "Patterns over problem counts: a week-by-week plan that covers arrays to dynamic programming with time left for mock interviews.",
		tags:    []string{"topic:interviews"}},
	{typ: content.TypeBlog, slug: "why-markdown", title: "Why Our Articles Are Markdown, Not MDX", author: "meera", category: "guides", coverHue: 265,
		summary: "Safe for thousands of authors, cheap to render at a million pages, and readable on Android: the case for Markdown with directives.",
		tags:    []string{}},
	{typ: content.TypeBlog, slug: "spaced-practice", title: "Spaced Practice Beats Grinding", author: "ada", category: "dsa", coverHue: 330,
		summary: "Solving a problem again after a few days teaches more than ten new ones in a row. How to build that habit with study plans.",
		tags:    []string{"topic:interviews"}},
	{typ: content.TypeBlog, slug: "keyset-pagination", title: "Why Page 10,000 Loads as Fast as Page 1", author: "linus", category: "system-design/databases", coverHue: 185,
		summary: "OFFSET pagination reads and discards every earlier row. Keyset pagination seeks straight to the next page with an index.",
		tags:    []string{"topic:databases"}},
}

type collectionSpec struct {
	kind                     collection.Kind
	slug, title, description string
	author                   string
	items                    []string // "type:slug"
}

var collectionCatalog = []collectionSpec{
	{kind: collection.KindStudyPlan, slug: "dsa-essentials", author: "ada", title: "DSA Essentials in 4 Weeks",
		description: "The core patterns in a sensible order: read the article, then solve the problems that use it.",
		items: []string{"article:arrays-introduction", "article:hash-maps-explained", "problem:two-sum", "article:two-pointers-technique",
			"article:sliding-window-technique", "problem:longest-substring-without-repeating-characters", "article:binary-search", "problem:binary-search",
			"article:sorting-algorithms-compared", "problem:merge-intervals", "article:introduction-to-dynamic-programming", "problem:climbing-stairs",
			"article:breadth-first-search", "problem:number-of-islands"}},
	{kind: collection.KindStudyPlan, slug: "top-interview-10", author: "grace", title: "Top Interview 10",
		description: "Ten classic problems that cover the patterns interviewers ask about most.",
		items: []string{"problem:two-sum", "problem:valid-parentheses", "problem:binary-search", "problem:maximum-subarray", "problem:climbing-stairs",
			"problem:merge-intervals", "problem:longest-substring-without-repeating-characters", "problem:number-of-islands",
			"problem:top-k-frequent-elements", "problem:coin-change"}},
	{kind: collection.KindStudyPlan, slug: "dynamic-programming-starter", author: "grace", title: "Dynamic Programming Starter",
		description: "From your first recurrence to unbounded knapsack, one step at a time.",
		items:       []string{"article:introduction-to-dynamic-programming", "problem:climbing-stairs", "article:kadanes-algorithm", "problem:maximum-subarray", "problem:coin-change"}},
	{kind: collection.KindSeries, slug: "javascript-under-the-hood", author: "meera", title: "JavaScript Under the Hood",
		description: "How JavaScript really runs your code, from scope to the event loop.",
		items:       []string{"article:javascript-closures", "article:javascript-event-loop", "article:react-state-and-rendering"}},
}
