package maintenance

import (
	"context"
	"slices"
	"testing"
	"time"

	"code-sprout-cms/internal/platform/testenv"
)

// Every RANGE-partitioned table must be kept ahead here, or its inserts start failing once the
// months its migration created run out. A new partitioned table that is not listed fails this.
func TestEveryRangePartitionedTableIsKeptAhead(t *testing.T) {
	d := testenv.DB(t)
	rows, err := d.Primary.Query(context.Background(), `
		SELECT c.relname FROM pg_partitioned_table p
		JOIN pg_class c ON c.oid = p.partrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE p.partstrat = 'r' AND n.nspname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		n++
		if !slices.Contains(monthlyPartitioned, table) {
			t.Errorf("%s is range-partitioned but not in monthlyPartitioned", table)
		}
	}
	if n == 0 {
		t.Fatal("found no range-partitioned tables: the query is wrong")
	}
}

// After upkeep, each table has a partition for the month monthsAhead from now.
func TestEnsurePartitionsCreatesTheMonthsAhead(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	if err := EnsurePartitions(ctx, d.Primary); err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().UTC().AddDate(0, monthsAhead, 0)
	probe := time.Date(ahead.Year(), ahead.Month(), 15, 0, 0, 0, 0, time.UTC)
	for _, table := range monthlyPartitioned {
		var covered bool
		// The partition that starts on the first of that month.
		err := d.Primary.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_inherits i
				JOIN pg_class child ON child.oid = i.inhrelid
				WHERE i.inhparent = $1::regclass
				  AND pg_get_expr(child.relpartbound, child.oid) LIKE 'FOR VALUES FROM (''' || to_char($2::date, 'YYYY-MM') || '-01%')`,
			table, probe).Scan(&covered)
		if err != nil {
			t.Fatal(err)
		}
		if !covered {
			t.Errorf("%s has no partition for %s", table, probe.Format("2006-01"))
		}
	}
}
