package access_test

import (
	"context"
	"errors"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/testenv"
)

// category inserts a node under parent (nil = a new root) and returns its id. Slugs come from
// testenv.Unique, so tests sharing the database never collide.
func category(t *testing.T, d *db.DB, parent *int64, parentPath string) (int64, string) {
	t.Helper()
	slug := testenv.Unique("acc")
	label := ""
	for _, r := range slug {
		if r == '-' {
			r = '_'
		}
		label += string(r)
	}
	path := label
	if parentPath != "" {
		path = parentPath + "." + label
	}
	var id int64
	if err := d.Primary.QueryRow(context.Background(),
		`INSERT INTO category (parent_id, path, slug, name) VALUES ($1, $2::ltree, $3, $3) RETURNING id`, parent, path, slug).Scan(&id); err != nil {
		t.Fatalf("category: %v", err)
	}
	return id, path
}

func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// Higher roles satisfy lower checks; a scoped grant covers its whole subtree and nothing beside it.
func TestRolesAreHierarchicalAndScopedToSubtrees(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	pol := access.NewPolicy(d.Primary)
	root, rootPath := category(t, d, nil, "")
	child, _ := category(t, d, &root, rootPath)
	sibling, _ := category(t, d, nil, "")

	editor := testenv.User(t, d)
	testenv.Grant(t, d, editor, "EDITOR", &root)

	cases := []struct {
		name  string
		p     auth.Principal
		role  access.Role
		scope *int64
		want  bool
	}{
		{"editor reviews in the subtree root", editor, access.RoleReviewer, &root, true},
		{"editor edits a descendant", editor, access.RoleEditor, &child, true},
		{"editor is not admin there", editor, access.RoleAdmin, &child, false},
		{"scope does not reach a sibling", editor, access.RoleReviewer, &sibling, false},
		{"scope is not global", editor, access.RoleReviewer, nil, false},
		{"site owner passes everything", testenv.Admin(t, d), access.RoleAdmin, &sibling, true},
		{"plain user has no CMS role", testenv.User(t, d), access.RoleContributor, &root, false},
	}
	for _, c := range cases {
		got, err := pol.Has(ctx, c.p, c.role, c.scope)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: Has = %v, want %v", c.name, got, c.want)
		}
	}
	if err := pol.Require(ctx, editor, access.RoleAdmin, nil); code(err) != "access.denied" {
		t.Errorf("Require refusal = %v, want access.denied", err)
	}
}

// Anyone with a real account may write drafts; guests may not.
func TestGuestsCannotAuthor(t *testing.T) {
	if err := access.RequireAuthor(auth.Principal{UserID: 1}); err != nil {
		t.Errorf("full account refused: %v", err)
	}
	if err := access.RequireAuthor(auth.Principal{UserID: 1, IsGuest: true}); code(err) != "access.guest" {
		t.Errorf("guest allowed: %v", err)
	}
}

// Only the site owner hands out ADMIN; a CMS admin manages lower roles; anyone else is refused,
// and a refused revoke leaves the grant in place.
func TestGrantingRolesNeedsTheRightAuthority(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	grants := access.NewGrants(d, access.NewPolicy(d.Primary))
	owner := testenv.Admin(t, d)
	cmsAdmin := testenv.User(t, d)
	testenv.Grant(t, d, cmsAdmin, "ADMIN", nil)
	editor := testenv.User(t, d)
	testenv.Grant(t, d, editor, "EDITOR", nil)
	target := testenv.User(t, d)

	if _, err := grants.Add(ctx, cmsAdmin, target.UserID, access.RoleAdmin, nil); code(err) != "role.admin_only" {
		t.Errorf("CMS admin granting ADMIN: %v, want role.admin_only", err)
	}
	g, err := grants.Add(ctx, cmsAdmin, target.UserID, access.RoleEditor, nil)
	if err != nil {
		t.Fatalf("CMS admin granting EDITOR: %v", err)
	}
	if _, err := grants.Add(ctx, cmsAdmin, target.UserID, access.RoleEditor, nil); code(err) != "role.exists" {
		t.Errorf("duplicate grant: %v, want role.exists", err)
	}
	if _, err := grants.Add(ctx, editor, target.UserID, access.RoleReviewer, nil); code(err) != "access.denied" {
		t.Errorf("editor granting: %v, want access.denied", err)
	}
	if err := grants.Remove(ctx, editor, g.ID); code(err) != "access.denied" {
		t.Errorf("editor revoking: %v, want access.denied", err)
	}
	if ok, _ := access.NewPolicy(d.Primary).Has(ctx, target, access.RoleEditor, nil); !ok {
		t.Fatal("a refused revoke removed the grant")
	}
	if _, err := grants.Add(ctx, owner, target.UserID, access.RoleAdmin, nil); err != nil {
		t.Errorf("site owner granting ADMIN: %v", err)
	}
	if err := grants.Remove(ctx, cmsAdmin, g.ID); err != nil {
		t.Errorf("CMS admin revoking EDITOR: %v", err)
	}
}
