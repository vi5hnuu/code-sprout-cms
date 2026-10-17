package content

import (
	"context"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content/contentdb"
)

// Actions are the studio steps the caller may take on an item right now. They mirror the
// role and state checks the endpoints enforce, so clients show the right controls without
// re-implementing category-scoped policy. Publishability (summary, category, Markdown issues)
// is not part of it: those blockers are reported as issues when the step is attempted.
type Actions struct {
	Edit     bool `json:"edit"`
	Submit   bool `json:"submit"`
	Withdraw bool `json:"withdraw"`
	Review   bool `json:"review"`
	Publish  bool `json:"publish"`
	Schedule bool `json:"schedule"`
	Archive  bool `json:"archive"`
	Delete   bool `json:"delete"`
}

func (s *Service) actionsFor(ctx context.Context, p auth.Principal, c contentdb.Content, meta Meta) (Actions, error) {
	scope := scopeOf(c, meta)
	editor, err := s.policy.Has(ctx, p, access.RoleEditor, scope)
	if err != nil {
		return Actions{}, err
	}
	reviewer, err := s.policy.Has(ctx, p, access.RoleReviewer, scope)
	if err != nil {
		return Actions{}, err
	}
	author := c.AuthorID == p.UserID && !p.IsGuest
	rs, st := ReviewState(c.ReviewState), Status(c.Status)
	liveIsDraft := st == StatusPublished && c.PublishedRevNo != nil && *c.PublishedRevNo == c.DraftRevNo
	return Actions{
		Edit:     editor || (author && !rs.locked()),
		Submit:   (author || editor) && (rs == ReviewNone || rs == ReviewChangesRequested) && !liveIsDraft,
		Withdraw: (author || editor) && rs.locked(),
		Review:   reviewer && rs == ReviewInReview && (c.AuthorID != p.UserID || p.IsAdmin),
		Publish:  editor && !liveIsDraft,
		Schedule: editor && (rs == ReviewApproved || rs == ReviewScheduled),
		Archive:  editor && st == StatusPublished,
		Delete:   editor || (c.AuthorID == p.UserID && st == StatusDraft),
	}, nil
}
