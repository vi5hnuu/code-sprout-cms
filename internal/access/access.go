// Package access answers "may this principal do X here?" for every domain, so authorization
// rules live in one place instead of being re-implemented per handler.
//
// Model:
//   - Auth's ROLE_ADMIN is a super-user: every check passes.
//   - CMS roles are hierarchical. ADMIN ⊇ EDITOR ⊇ REVIEWER ⊇ CONTRIBUTOR: a grant of a
//     higher role satisfies a check for any lower one.
//   - A grant is global or scoped to a category subtree (e.g. EDITOR of dsa covers
//     dsa.algorithms.searching).
//   - Any signed-in, non-guest user may author drafts (GFG-style open contribution); review and
//     publishing need explicit grants.
package access

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/access/accessdb"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
)

type Role string

const (
	RoleContributor Role = "CONTRIBUTOR"
	RoleReviewer    Role = "REVIEWER"
	RoleEditor      Role = "EDITOR"
	RoleAdmin       Role = "ADMIN"
)

// satisfiedBy lists, for each role, every role whose grant satisfies it.
var satisfiedBy = map[Role][]string{
	RoleContributor: {"CONTRIBUTOR", "REVIEWER", "EDITOR", "ADMIN"},
	RoleReviewer:    {"REVIEWER", "EDITOR", "ADMIN"},
	RoleEditor:      {"EDITOR", "ADMIN"},
	RoleAdmin:       {"ADMIN"},
}

type Policy struct{ q *accessdb.Queries }

func NewPolicy(primary *pgxpool.Pool) *Policy { return &Policy{q: accessdb.New(primary)} }

// Has reports whether p holds at least `role` globally or over categoryID (nil = global only).
func (pol *Policy) Has(ctx context.Context, p auth.Principal, role Role, categoryID *int64) (bool, error) {
	if p.IsAdmin {
		return true, nil
	}
	ok, err := pol.q.HasRoleFor(ctx, accessdb.HasRoleForParams{
		UserID: p.UserID, Roles: satisfiedBy[role], CategoryID: categoryID,
	})
	if err != nil {
		return false, fmt.Errorf("access check %s: %w", role, err)
	}
	return ok, nil
}

// Require is Has that turns a refusal into a 403.
func (pol *Policy) Require(ctx context.Context, p auth.Principal, role Role, categoryID *int64) error {
	ok, err := pol.Has(ctx, p, role, categoryID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Forbidden("access.denied", fmt.Sprintf("This needs the %s role.", role))
	}
	return nil
}

// RequireAuthor allows any signed-in, non-guest account to create and edit its own drafts.
func RequireAuthor(p auth.Principal) error {
	if p.IsGuest {
		return apperr.Forbidden("access.guest", "Create a free account to write content.")
	}
	return nil
}
