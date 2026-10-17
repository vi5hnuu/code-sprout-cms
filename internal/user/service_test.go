package user

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/testenv"
)

func uniqueSub(t *testing.T) string {
	return fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
}

func TestResolveIDProvisionsOnceUnderConcurrency(t *testing.T) {
	d := testenv.DB(t)
	sub := uniqueSub(t)
	ctx := context.Background()

	// Many first requests from the same user race to create the row; all must agree on one id.
	const callers = 20
	ids := make([]int64, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			// Separate services → separate caches, so every caller really hits the database.
			id, err := NewService(d.Primary).ResolveID(ctx, sub, "Asha")
			if err != nil {
				t.Errorf("resolve: %v", err)
			}
			ids[i] = id
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id == 0 || id != ids[0] {
			t.Fatalf("callers disagreed on the user id: %v", ids)
		}
	}

	var rows int
	if err := d.Primary.QueryRow(ctx, "SELECT count(*) FROM app_user WHERE auth_sub = $1", sub).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("app_user rows = %d, want 1", rows)
	}
}

func TestProfile(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d.Primary)
	ctx := context.Background()

	id, err := svc.ResolveID(ctx, uniqueSub(t), "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Primary.Exec(ctx, "INSERT INTO cms_role_grant (user_id, role) VALUES ($1, 'EDITOR')", id); err != nil {
		t.Fatal(err)
	}

	p, err := svc.Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "Sprout reader" {
		t.Fatalf("blank name should fall back, got %q", p.DisplayName)
	}
	if len(p.Roles) != 1 || p.Roles[0].Role != access.RoleEditor || p.Roles[0].ScopeCategoryID != nil {
		t.Fatalf("roles = %+v", p.Roles)
	}

	_, err = svc.Profile(ctx, -1)
	if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindNotFound {
		t.Fatalf("missing user: err = %v", err)
	}
}
