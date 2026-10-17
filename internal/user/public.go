package user

import (
	"context"
	"regexp"
	"strings"

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/user/userdb"
)

// Files resolves avatar ids to public files (implemented by asset.Service).
type Files interface {
	Describe(ctx context.Context, ids []int64) (map[int64]asset.File, error)
}

// PublicUser is what anyone may see about an author.
type PublicUser struct {
	ID          int64   `json:"id"`
	Handle      *string `json:"handle"` // nil until the user picks one; no /u/ page until then
	DisplayName string  `json:"displayName"`
	Bio         *string `json:"bio"`
	AvatarURL   *string `json:"avatarUrl"`
}

type UpdateProfile struct {
	Handle       *string `json:"handle"`
	DisplayName  string  `json:"displayName"`
	Bio          *string `json:"bio"`
	AvatarFileID *int64  `json:"avatarFileId"`
}

var handlePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{1,28}[a-z0-9])?$`)

// Handles that would shadow site routes or impersonate staff.
var reservedHandles = map[string]bool{
	"admin": true, "administrator": true, "api": true, "editor": true, "help": true, "me": true,
	"moderator": true, "root": true, "settings": true, "staff": true, "studio": true, "support": true,
	"sprout": true, "codesprout": true, "system": true,
}

// PublicProfiles batch-loads authors for listings and pages (missing ids are absent).
func (s *Service) PublicProfiles(ctx context.Context, ids []int64) (map[int64]PublicUser, error) {
	out := make(map[int64]PublicUser, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := userdb.New(s.replica).ListPublicUsers(ctx, ids)
	if err != nil {
		return nil, err
	}
	var avatarIDs []int64
	for _, r := range rows {
		if r.AvatarFileID != nil {
			avatarIDs = append(avatarIDs, *r.AvatarFileID)
		}
	}
	avatars, err := s.files.Describe(ctx, avatarIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		u := PublicUser{ID: r.ID, Handle: r.Handle, DisplayName: r.DisplayName, Bio: r.Bio}
		if r.AvatarFileID != nil {
			u.AvatarURL = avatars[*r.AvatarFileID].URL
		}
		out[r.ID] = u
	}
	return out, nil
}

func (s *Service) ByHandle(ctx context.Context, handle string) (PublicUser, error) {
	r, err := userdb.New(s.replica).GetUserByHandle(ctx, &handle)
	if db.IsNoRows(err) {
		return PublicUser{}, apperr.NotFound("user.not_found", "No such user.")
	}
	if err != nil {
		return PublicUser{}, err
	}
	m, err := s.PublicProfiles(ctx, []int64{r.ID})
	if err != nil {
		return PublicUser{}, err
	}
	return m[r.ID], nil
}

func (s *Service) UpdateProfile(ctx context.Context, id int64, in UpdateProfile) (Profile, error) {
	f := map[string]string{}
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if n := len([]rune(in.DisplayName)); n == 0 || n > 60 {
		f["displayName"] = "Display name is required and at most 60 characters."
	}
	if in.Handle != nil {
		h := strings.ToLower(strings.TrimSpace(*in.Handle))
		in.Handle = &h
		if !handlePattern.MatchString(h) || reservedHandles[h] {
			f["handle"] = "Use 3-30 lower-case letters, digits, '-' or '_' (not a reserved word)."
		}
	}
	if in.Bio != nil && len([]rune(*in.Bio)) > 300 {
		f["bio"] = "Bio is at most 300 characters."
	}
	if in.AvatarFileID != nil {
		files, err := s.files.Describe(ctx, []int64{*in.AvatarFileID})
		if err != nil {
			return Profile{}, err
		}
		if file, ok := files[*in.AvatarFileID]; !ok || file.Purpose != asset.PurposeAvatar {
			f["avatarFileId"] = "Upload the picture as an AVATAR and complete the upload first."
		}
	}
	if len(f) > 0 {
		return Profile{}, apperr.ValidationFailed(f)
	}
	_, err := s.q.UpdateProfile(ctx, userdb.UpdateProfileParams{
		ID: id, Handle: in.Handle, DisplayName: in.DisplayName, Bio: in.Bio, AvatarFileID: in.AvatarFileID,
	})
	if c, ok := db.ConstraintViolation(err); ok && c == "app_user_handle_key" {
		return Profile{}, apperr.Conflict("user.handle_taken", "That handle is taken.")
	}
	if err != nil {
		return Profile{}, err
	}
	return s.Profile(ctx, id)
}
