package access

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access/accessdb"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
)

// Grant is one CMS role held by a user, globally or over a category subtree.
type Grant struct {
	ID              int64     `json:"id"`
	UserID          int64     `json:"userId"`
	Role            Role      `json:"role"`
	ScopeCategoryID *int64    `json:"scopeCategoryId"`
	GrantedBy       *int64    `json:"grantedBy"`
	CreatedAt       time.Time `json:"createdAt"`
}

type GrantCursor struct {
	ID int64 `json:"id"`
}

// Grants administers role grants. Only a site-wide ADMIN may change them, and ADMIN itself is
// granted only by an auth super-user, so admins cannot multiply themselves.
type Grants struct {
	db     *db.DB
	policy *Policy
}

func NewGrants(d *db.DB, policy *Policy) *Grants { return &Grants{db: d, policy: policy} }

func grantFromRow(g accessdb.CmsRoleGrant) Grant {
	return Grant{ID: g.ID, UserID: g.UserID, Role: Role(g.Role), ScopeCategoryID: g.ScopeCategoryID, GrantedBy: g.GrantedBy, CreatedAt: g.CreatedAt}
}

// List pages through grants newest first; userID narrows it to one user.
func (g *Grants) List(ctx context.Context, p auth.Principal, userID *int64, req page.Request[GrantCursor]) (page.Page[Grant], error) {
	if err := g.policy.Require(ctx, p, RoleAdmin, nil); err != nil {
		return page.Page[Grant]{}, err
	}
	var after *int64
	if req.Key != nil {
		after = &req.Key.ID
	}
	rows, err := accessdb.New(g.db.Primary).ListGrants(ctx, accessdb.ListGrantsParams{UserID: userID, AfterID: after, RowLimit: int32(req.Limit + 1)})
	if err != nil {
		return page.Page[Grant]{}, err
	}
	out := make([]Grant, len(rows))
	for i, r := range rows {
		out[i] = grantFromRow(r)
	}
	return page.Build(out, req.Limit, func(x Grant) GrantCursor { return GrantCursor{ID: x.ID} }), nil
}

func (g *Grants) Add(ctx context.Context, p auth.Principal, userID int64, role Role, scope *int64) (Grant, error) {
	if _, ok := satisfiedBy[role]; !ok {
		return Grant{}, apperr.ValidationFailed(map[string]string{"role": "role must be CONTRIBUTOR, REVIEWER, EDITOR or ADMIN."})
	}
	if err := g.requireCanChange(ctx, p, role); err != nil {
		return Grant{}, err
	}
	var out Grant
	err := g.db.InTx(ctx, func(tx pgx.Tx) error {
		row, err := accessdb.New(tx).InsertGrant(ctx, accessdb.InsertGrantParams{UserID: userID, Role: string(role), ScopeCategoryID: scope, GrantedBy: &p.UserID})
		switch cv, _ := db.ConstraintViolation(err); cv {
		case "uq_cms_role_grant":
			return apperr.Conflict("role.exists", "This user already has that role there.")
		case "fk_cms_role_grant_scope":
			return apperr.ValidationFailed(map[string]string{"scopeCategoryId": "No such category."})
		}
		if err != nil {
			return err
		}
		out = grantFromRow(row)
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "role.grant", Entity: "user", EntityID: userID,
			Data: map[string]any{"role": role, "scopeCategoryId": scope}})
	})
	return out, err
}

func (g *Grants) Remove(ctx context.Context, p auth.Principal, id int64) error {
	return g.db.InTx(ctx, func(tx pgx.Tx) error {
		q := accessdb.New(tx)
		row, err := q.DeleteGrant(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("role.not_found", "No such role grant.")
		}
		if err != nil {
			return err
		}
		// Checked against the deleted row inside the transaction: a refusal rolls it back.
		if err := g.requireCanChange(ctx, p, Role(row.Role)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "role.revoke", Entity: "user", EntityID: row.UserID,
			Data: map[string]any{"role": row.Role, "scopeCategoryId": row.ScopeCategoryID}})
	})
}

func (g *Grants) requireCanChange(ctx context.Context, p auth.Principal, role Role) error {
	if role == RoleAdmin && !p.IsAdmin {
		return apperr.Forbidden("role.admin_only", "Only a site owner can grant or revoke ADMIN.")
	}
	return g.policy.Require(ctx, p, RoleAdmin, nil)
}
