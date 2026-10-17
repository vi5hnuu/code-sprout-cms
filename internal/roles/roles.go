// Package roles is the admin surface for CMS role grants. The access package owns the grants
// and their rules; this package resolves who and where (user profiles, category paths) for
// display, which access cannot do itself without importing the domains that depend on it.
package roles

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

type Users interface {
	ByHandle(ctx context.Context, handle string) (user.PublicUser, error)
	PublicProfiles(ctx context.Context, ids []int64) (map[int64]user.PublicUser, error)
}

type Categories interface {
	CategoriesByID(ctx context.Context, ids []int64) (map[int64]taxonomy.Category, error)
}

// Scope is the category subtree a grant covers.
type Scope struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// View is a grant as the admin screen shows it.
type View struct {
	ID        int64           `json:"id"`
	User      user.PublicUser `json:"user"`
	Role      access.Role     `json:"role"`
	Scope     *Scope          `json:"scope"` // nil: site-wide
	GrantedBy *int64          `json:"grantedBy"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Handler struct {
	grants     *access.Grants
	users      Users
	categories Categories
}

func NewHandler(grants *access.Grants, users Users, categories Categories) *Handler {
	return &Handler{grants: grants, users: users, categories: categories}
}

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/admin/roles", authed(httpx.HandlerFunc(h.list)))
	mux.Handle("POST /v1/admin/roles", authed(httpx.HandlerFunc(h.grant)))
	mux.Handle("DELETE /v1/admin/roles/{id}", authed(httpx.HandlerFunc(h.revoke)))
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// list answers ?user=<handle> for one user's grants, or every grant newest first.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[access.GrantCursor](r)
	if err != nil {
		return err
	}
	var userID *int64
	if handle := strings.TrimSpace(r.URL.Query().Get("user")); handle != "" {
		u, err := h.users.ByHandle(r.Context(), handle)
		if err != nil {
			return err
		}
		userID = &u.ID
	}
	res, err := h.grants.List(r.Context(), principal(r), userID, req)
	if err != nil {
		return err
	}
	views, err := h.describe(r.Context(), res.Items)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, page.Page[View]{Items: views, NextCursor: res.NextCursor})
	return nil
}

type grantInput struct {
	Handle          string      `json:"handle"`
	UserID          int64       `json:"userId"`
	Role            access.Role `json:"role"`
	ScopeCategoryID *int64      `json:"scopeCategoryId"`
}

// grant takes the user by handle (what admins know) or by id (users without a handle yet).
func (h *Handler) grant(w http.ResponseWriter, r *http.Request) error {
	var in grantInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	userID := in.UserID
	if handle := strings.TrimSpace(in.Handle); handle != "" {
		u, err := h.users.ByHandle(r.Context(), handle)
		if err != nil {
			return err
		}
		userID = u.ID
	}
	if userID <= 0 {
		return apperr.ValidationFailed(map[string]string{"handle": "Give the user's handle or id."})
	}
	if found, err := h.users.PublicProfiles(r.Context(), []int64{userID}); err != nil {
		return err
	} else if _, ok := found[userID]; !ok {
		return apperr.NotFound("user.not_found", "No such user.")
	}
	g, err := h.grants.Add(r.Context(), principal(r), userID, in.Role, in.ScopeCategoryID)
	if err != nil {
		return err
	}
	views, err := h.describe(r.Context(), []access.Grant{g})
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, views[0])
	return nil
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return apperr.NotFound("role.not_found", "No such role grant.")
	}
	if err := h.grants.Remove(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

// describe resolves the users and categories of a page of grants in two batched lookups.
func (h *Handler) describe(ctx context.Context, gs []access.Grant) ([]View, error) {
	userIDs := make([]int64, 0, len(gs))
	catIDs := make([]int64, 0, len(gs))
	for _, g := range gs {
		userIDs = append(userIDs, g.UserID)
		if g.ScopeCategoryID != nil {
			catIDs = append(catIDs, *g.ScopeCategoryID)
		}
	}
	users, err := h.users.PublicProfiles(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	cats, err := h.categories.CategoriesByID(ctx, catIDs)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(gs))
	for i, g := range gs {
		v := View{ID: g.ID, User: users[g.UserID], Role: g.Role, GrantedBy: g.GrantedBy, CreatedAt: g.CreatedAt}
		if v.User.ID == 0 {
			v.User.ID = g.UserID
		}
		if g.ScopeCategoryID != nil {
			if c, ok := cats[*g.ScopeCategoryID]; ok {
				v.Scope = &Scope{ID: c.ID, Path: c.Path, Name: c.Name}
			}
		}
		out[i] = v
	}
	return out, nil
}
