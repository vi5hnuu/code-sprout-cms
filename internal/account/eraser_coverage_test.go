package account_test

import (
	"context"
	"sort"
	"testing"

	"code-sprout-cms/internal/platform/testenv"
)

// This test is the DPDPA/GDPR safety net for account erasure.
//
// Every table that references a user — directly via `user_id`, or indirectly via `author_id`,
// `editor_id`, `actor_id`, `owner_id`, `reviewer_id` — must be covered by one of the eraser
// functions wired into `account.NewService` (see cmd/api/main.go). If a new partitioned or
// per-user table lands and the author forgets to add an eraser, this test fails loudly instead
// of silently leaking that reader's personal data on account deletion.
//
// How to make a failure pass:
//   1. If the new table is personal data: add a DELETE query, a `EraseUser` function in the
//      owning domain, and register it in `cmd/api/main.go`'s `account.NewService(...)` call.
//      Then add the table to `expectedCoveredTables` below.
//   2. If the column really isn't personal (e.g. "created by admin" audit metadata that stays
//      with the content), add the table to `nonPersonalColumns` with a comment explaining why.
//
// The allow-list style makes "new column that we missed" the loud default instead of silently
// true-adding to the list.
func TestEraserCoversEveryUserTiedTable(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()

	rows, err := d.Primary.Query(ctx, `
		SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables  t ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public'
		  AND t.table_type   = 'BASE TABLE'
		  -- hash-partition children (comment_pNN, reaction_pNN, user_note_pNN, …) inherit the parent
		  AND c.table_name !~ '_p[0-9]+$'
		  AND c.column_name IN ('user_id', 'author_id', 'editor_id', 'actor_id', 'owner_id', 'reviewer_id')
		ORDER BY c.table_name, c.column_name;`)
	if err != nil {
		t.Fatalf("list user-tied columns: %v", err)
	}
	defer rows.Close()

	seen := map[string]map[string]bool{} // table → set-of-columns
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := seen[table]; !ok {
			seen[table] = map[string]bool{}
		}
		seen[table][col] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	for table, cols := range seen {
		for col := range cols {
			covered, nonPersonal := isCovered(table, col), isNonPersonal(table, col)
			if !covered && !nonPersonal {
				var all []string
				for c := range cols {
					all = append(all, c)
				}
				sort.Strings(all)
				t.Errorf("%s.%s is user-tied but neither in expectedCoveredTables nor in nonPersonalColumns (all user-ish columns on this table: %v)."+
					"\n Add an EraseUser query that touches it, register it in cmd/api/main.go, add it to expectedCoveredTables; OR document why it is non-personal and add to nonPersonalColumns.",
					table, col, all)
			}
		}
	}
}

// expectedCoveredTables are the (table, column) pairs the current erasers already touch.
// Every entry must have a corresponding DELETE / anonymise step in one of the domain's
// erase.go files. Grep `grep -rn 'FROM <table>\|UPDATE <table>' internal/*/erase.go` to find it.
var expectedCoveredTables = map[string]map[string]string{
	"app_user": {
		// users.EraseUser anonymises display name / email / avatar and detaches google_sub.
		"id": "internal/user/service.go EraseUser",
	},
	"bookmark":           {"user_id": "internal/library/erase.go"},
	"bookmark_list":      {"user_id": "internal/library/erase.go"},
	"reaction":           {"user_id": "internal/engagement/erase.go"},
	"user_note":          {"user_id": "internal/library/erase.go"},
	"user_review":        {"user_id": "internal/library/erase.go"},
	"user_problem":       {"user_id": "internal/submission/erase.go"},
	"user_activity_day":  {"user_id": "internal/library/erase.go"},
	"submission":         {"user_id": "internal/submission/erase.go"},
	"comment":            {"author_id": "internal/engagement/erase.go (anonymises visible, deletes drafts)"},
	"content":            {"author_id": "stays, re-attributed to 'former-member' at erase time"},
	"content_revision":   {"editor_id": "stays — revisions are history; editor_id is 'former-member' after erase"},
	"audit_log":          {"actor_id": "stays — audit trail is append-only; actor_id is 'former-member' after erase"},
	"cms_role_grant":     {"user_id": "internal/access/erase.go — role grants deleted"},
	"collection":         {"owner_id": "internal/collection/erase.go — collections re-owned by former-member or deleted"},
	"file":               {"owner_id": "internal/asset/erase.go — avatars MARKED DELETING; article images stay (public, immutable asset-id refs)"},
	"comment_report":     {"reporter_id": "internal/engagement/erase.go"},
}

// nonPersonalColumns are deliberately kept after erase. Document the reason inline.
var nonPersonalColumns = map[string]map[string]string{
	// Nothing yet — every user-tied column so far is either personal (handled) or part of the
	// append-only audit/revisions trail (handled with re-attribution to 'former-member').
}

func isCovered(table, col string) bool {
	m, ok := expectedCoveredTables[table]
	return ok && m[col] != ""
}

func isNonPersonal(table, col string) bool {
	m, ok := nonPersonalColumns[table]
	return ok && m[col] != ""
}
