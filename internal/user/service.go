// Package user owns app_user: the CMS-side mirror of an auth account (profile, handle) and the
// editorial role grants attached to it.
package user

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/user/userdb"
)

type RoleGrant struct {
	Role            access.Role `json:"role"`
	ScopeCategoryID *int64      `json:"scopeCategoryId,omitempty"` // nil = everywhere
}

type Profile struct {
	ID           int64       `json:"id"`
	Handle       *string     `json:"handle"`
	DisplayName  string      `json:"displayName"`
	Bio          *string     `json:"bio"`
	AvatarFileID *int64      `json:"avatarFileId"`
	CreatedAt    time.Time   `json:"createdAt"`
	Roles        []RoleGrant `json:"roles"`
}

type Service struct {
	q       *userdb.Queries
	replica *pgxpool.Pool
	files   Files
	// auth sub → app_user.id. Mappings never change once created, so caching is safe; the
	// bound keeps memory flat however many distinct users we see.
	ids *expirable.LRU[string, int64]
}

func NewService(primary, replica *pgxpool.Pool, files Files) *Service {
	return &Service{
		q:       userdb.New(primary),
		replica: replica,
		files:   files,
		ids:     expirable.NewLRU[string, int64](100_000, nil, time.Hour),
	}
}

// ResolveID returns the local id for an auth subject, creating the app_user row on the
// user's first authenticated request (just-in-time provisioning).
func (s *Service) ResolveID(ctx context.Context, sub, displayName string) (int64, error) {
	if id, ok := s.ids.Get(sub); ok {
		return id, nil
	}
	id, err := s.q.FindUserIDBySub(ctx, sub)
	if db.IsNoRows(err) {
		id, err = s.q.InsertUser(ctx, userdb.InsertUserParams{AuthSub: sub, DisplayName: fallbackDisplayName(displayName)})
		if db.IsNoRows(err) { // lost the insert race to a concurrent request; the row exists now
			id, err = s.q.FindUserIDBySub(ctx, sub)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve user %q: %w", sub, err)
	}
	s.ids.Add(sub, id)
	return id, nil
}

func (s *Service) Profile(ctx context.Context, id int64) (Profile, error) {
	u, err := s.q.GetUser(ctx, id)
	if db.IsNoRows(err) {
		return Profile{}, apperr.NotFound("user.not_found", "User not found.")
	}
	if err != nil {
		return Profile{}, err
	}
	grants, err := s.q.ListRoleGrants(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	roles := make([]RoleGrant, len(grants))
	for i, g := range grants {
		roles[i] = RoleGrant{Role: access.Role(g.Role), ScopeCategoryID: g.ScopeCategoryID}
	}
	return Profile{
		ID:           u.ID,
		Handle:       u.Handle,
		DisplayName:  u.DisplayName,
		Bio:          u.Bio,
		AvatarFileID: u.AvatarFileID,
		CreatedAt:    u.CreatedAt,
		Roles:        roles,
	}, nil
}

// Guests from auth carry no username; give them a neutral name until they set one.
func fallbackDisplayName(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return "Sprout reader"
}
