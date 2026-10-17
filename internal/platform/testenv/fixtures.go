package testenv

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/db"
)

var seq atomic.Int64

// Unique returns a slug-safe token unique within the test run, so tests sharing the test
// database never collide on unique columns.
func Unique(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano()%1_000_000_000, seq.Add(1))
}

// User inserts an app_user and returns a principal for it.
func User(t *testing.T, d *db.DB) auth.Principal {
	t.Helper()
	sub := Unique("sub")
	var id int64
	if err := d.Primary.QueryRow(context.Background(),
		"INSERT INTO app_user (auth_sub, display_name) VALUES ($1, 'Test user') RETURNING id", sub).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return auth.Principal{UserID: id, Subject: sub}
}

// Admin is a user with auth's ROLE_ADMIN.
func Admin(t *testing.T, d *db.DB) auth.Principal {
	p := User(t, d)
	p.IsAdmin = true
	return p
}

// Grant gives p a CMS role, optionally scoped to a category.
func Grant(t *testing.T, d *db.DB, p auth.Principal, role string, scopeCategoryID *int64) {
	t.Helper()
	if _, err := d.Primary.Exec(context.Background(),
		"INSERT INTO cms_role_grant (user_id, role, scope_category_id) VALUES ($1, $2, $3)", p.UserID, role, scopeCategoryID); err != nil {
		t.Fatalf("grant %s: %v", role, err)
	}
}
