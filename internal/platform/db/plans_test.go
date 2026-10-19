package db_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/platform/testenv"
)

// largeTables grow with readers or content (millions of rows). A read of one of them must be
// able to use an index; a sequential scan that is fine on a seeded database becomes an outage
// at scale. Partitions are named <table>_p<n> or <table>_y<year>m<month>, so a prefix matches.
var largeTables = []string{
	"content", "content_revision", "content_tag", "content_link", "comment", "reaction", "bookmark",
	"bookmark_list", "collection_item", "submission", "user_problem", "user_review", "user_note",
	"user_activity_day", "file", "outbox", "audit_log", "content_daily_stats",
}

// allowedScans are queries that may scan a large table, each with the reason. Empty today: every
// read is index-backed. An entry needs a reason a reviewer would accept.
var allowedScans = map[string]string{}

var (
	generatedQuery  = regexp.MustCompile("(?s)const \\w+ = `-- name: (\\w+) :(\\w+)\n(.*?)`")
	seqScan         = regexp.MustCompile(`Seq Scan on (\w+)`)
	partitionSuffix = regexp.MustCompile(`_(p\d+|y\d{4}m\d{2}|default)$`)
)

// Every read query sqlc generated must reach large tables through an index. Plans are generic
// (no parameter values) with sequential scans disabled: a query that still shows one has no
// usable index at all. New queries are checked automatically.
func TestReadQueriesUseIndexesOnLargeTables(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	files, err := filepath.Glob("../../*/*db/*.sql.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no generated query files found (%v)", err)
	}
	// The detector itself: a filter no index serves must show up as a sequential scan.
	if got := explainScans(ctx, t, d.Primary, "self-check", "SELECT id FROM content WHERE summary = $1"); len(got) == 0 {
		t.Fatal("an unindexed filter was not reported: the plan check is not working")
	}
	checked := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range generatedQuery.FindAllStringSubmatch(string(src), -1) {
			name, kind, sql := m[1], m[2], m[3]
			head := strings.ToUpper(strings.TrimSpace(sql))
			if (kind != "one" && kind != "many") || !(strings.HasPrefix(head, "SELECT") || strings.HasPrefix(head, "WITH")) {
				continue
			}
			if _, ok := allowedScans[name]; ok {
				continue
			}
			checked++
			scanned := explainScans(ctx, t, d.Primary, name, sql)
			for _, table := range scanned {
				if isLarge(table) {
					t.Errorf("%s (%s) scans %s sequentially: add an index or list it in allowedScans with a reason", name, filepath.Base(f), table)
				}
			}
		}
	}
	t.Logf("checked %d read queries", checked)
	if checked < 50 {
		t.Fatalf("checked only %d queries: the extraction is broken", checked)
	}
}

// explainScans returns the tables (or partitions) the query's generic plan scans sequentially.
func explainScans(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name, sql string) []string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only; nothing to keep
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	// The simple-query protocol sends the text as is: pgx would otherwise count the $n
	// placeholders and demand values, which a generic plan does not need.
	results, err := tx.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN) "+sql).ReadAll()
	if err != nil {
		t.Errorf("%s: explain failed: %v", name, err)
		return nil
	}
	var scanned []string
	for _, r := range results {
		for _, row := range r.Rows {
			for _, m := range seqScan.FindAllStringSubmatch(string(row[0]), -1) {
				scanned = append(scanned, m[1])
			}
		}
	}
	return unique(scanned)
}

func isLarge(table string) bool {
	base := partitionSuffix.ReplaceAllString(table, "")
	for _, l := range largeTables {
		if base == l {
			return true
		}
	}
	return false
}

func unique(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
