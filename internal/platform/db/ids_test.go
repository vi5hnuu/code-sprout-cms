package db_test

import (
	"context"
	"testing"

	"code-sprout-cms/internal/platform/testenv"
)

// Every identity sequence must live inside this database's shard range (see
// db/migrations/00011_id_ranges.sql), which also keeps every id below 2^53, the largest
// integer a JavaScript number holds exactly. A migration that adds an identity column without
// calling assign_id_range(current_id_shard()) fails here.
func TestIdentitySequencesStayInShardRange(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	var shard int64
	if err := d.Primary.QueryRow(ctx, `SELECT current_id_shard()`).Scan(&shard); err != nil {
		t.Fatal(err)
	}
	lo, hi := shard<<47+1, (shard+1)<<47-1
	rows, err := d.Primary.Query(ctx, `
		SELECT format('%I.%I', s.schemaname, s.sequencename), s.min_value, s.max_value
		FROM pg_depend dep
		JOIN pg_class c ON c.oid = dep.objid AND c.relkind = 'S'
		JOIN pg_sequences s ON format('%I.%I', s.schemaname, s.sequencename)::regclass = dep.objid::regclass
		WHERE dep.deptype = 'i' AND dep.classid = 'pg_class'::regclass`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var minV, maxV int64
		if err := rows.Scan(&name, &minV, &maxV); err != nil {
			t.Fatal(err)
		}
		n++
		if minV != lo || maxV != hi {
			t.Errorf("%s ranges %d..%d, want shard %d's %d..%d", name, minV, maxV, shard, lo, hi)
		}
		if maxV > 1<<53-1 {
			t.Errorf("%s can exceed 2^53-1 (JavaScript-safe integers)", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("found no identity sequences: the query is wrong")
	}
}
