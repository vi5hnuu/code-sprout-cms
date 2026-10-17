package engagement

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/user"
)

// Threads are shown two levels deep (LeetCode-style): top-level comments, each with a flat list
// of replies. A reply to a reply joins the same thread; ParentID records who it answers.

type CommentStatus string

const (
	CommentVisible CommentStatus = "VISIBLE"
	CommentHidden  CommentStatus = "HIDDEN"  // moderated or auto-hidden by reports
	CommentDeleted CommentStatus = "DELETED" // by its author or a moderator
)

// autoHideReports hides a comment pending moderation once this many readers report it.
const autoHideReports = 3

type Comment struct {
	ID         int64            `json:"id"`
	ContentID  int64            `json:"contentId"`
	RootID     *int64           `json:"rootId"`
	ParentID   *int64           `json:"parentId"`
	Author     *user.PublicUser `json:"author"` // nil for deleted comments
	Body       string           `json:"body"`   // empty for deleted comments
	Deleted    bool             `json:"deleted"`
	ReplyCount int32            `json:"replyCount"`
	CreatedAt  time.Time        `json:"createdAt"`
	EditedAt   *time.Time       `json:"editedAt"`
}

type CommentCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

func commentCursor(c Comment) CommentCursor { return CommentCursor{At: c.CreatedAt, ID: c.ID} }

func validateCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", apperr.ValidationFailed(map[string]string{"body": "Write something first."})
	}
	if a := markdown.AnalyzeComment([]byte(body)); !a.Valid() {
		return "", &apperr.Error{Kind: apperr.KindInvalid, Code: "comment.invalid",
			Detail: a.Issues[0].Message, Details: map[string]any{"issues": a.Issues}}
	}
	return body, nil
}

// requireCommenter: commenting needs a full (non-guest) account, which keeps anonymous
// throwaway accounts from flooding threads.
func requireCommenter(p auth.Principal) error {
	if p.IsGuest {
		return apperr.Forbidden("comment.guest", "Create a free account to comment.")
	}
	return nil
}

func (s *Service) CreateComment(ctx context.Context, p auth.Principal, contentID int64, parentID *int64, body string) (Comment, error) {
	if err := requireCommenter(p); err != nil {
		return Comment{}, err
	}
	body, err := validateCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	if err := s.requireLive(ctx, contentID); err != nil {
		return Comment{}, err
	}
	var created engagementdb.Comment
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		var rootID *int64
		if parentID != nil {
			parent, err := q.GetComment(ctx, engagementdb.GetCommentParams{ContentID: contentID, ID: *parentID})
			if db.IsNoRows(err) || (err == nil && CommentStatus(parent.Status) != CommentVisible) {
				return apperr.ValidationFailed(map[string]string{"parentId": "That comment is not available to reply to."})
			}
			if err != nil {
				return err
			}
			rootID = parent.RootID
			if rootID == nil {
				rootID = &parent.ID
			}
		}
		created, err = q.InsertComment(ctx, engagementdb.InsertCommentParams{
			ContentID: contentID, RootID: rootID, ParentID: parentID, AuthorID: p.UserID, BodyMd: body,
		})
		if err != nil {
			return err
		}
		if rootID != nil {
			if err := q.BumpReplyCount(ctx, engagementdb.BumpReplyCountParams{ContentID: contentID, ID: *rootID, Delta: 1}); err != nil {
				return err
			}
		}
		if err := applyDeltas(ctx, q, contentID, deltas{comments: 1}); err != nil {
			return err
		}
		return outbox.Publish(ctx, tx, outbox.TopicCommentsChanged, contentID, outbox.CommentsEvent{ContentID: contentID})
	})
	if err != nil {
		return Comment{}, err
	}
	out, err := s.hydrate(ctx, []engagementdb.Comment{created})
	if err != nil {
		return Comment{}, err
	}
	return out[0], nil
}

func (s *Service) EditComment(ctx context.Context, p auth.Principal, contentID, id int64, body string) (Comment, error) {
	body, err := validateCommentBody(body)
	if err != nil {
		return Comment{}, err
	}
	var edited engagementdb.Comment
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		c, err := s.lockOwnComment(ctx, q, p, contentID, id)
		if err != nil {
			return err
		}
		if CommentStatus(c.Status) != CommentVisible {
			return apperr.Conflict("comment.not_editable", "This comment can no longer be edited.")
		}
		if edited, err = q.EditComment(ctx, engagementdb.EditCommentParams{ContentID: contentID, ID: id, BodyMd: body}); err != nil {
			return err
		}
		return outbox.Publish(ctx, tx, outbox.TopicCommentsChanged, contentID, outbox.CommentsEvent{ContentID: contentID})
	})
	if err != nil {
		return Comment{}, err
	}
	out, err := s.hydrate(ctx, []engagementdb.Comment{edited})
	if err != nil {
		return Comment{}, err
	}
	return out[0], nil
}

// DeleteComment removes the caller's own comment, or any comment for a moderator.
func (s *Service) DeleteComment(ctx context.Context, p auth.Principal, contentID, id int64) error {
	mod, err := s.isModerator(ctx, p)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		c, err := q.GetCommentForUpdate(ctx, engagementdb.GetCommentForUpdateParams{ContentID: contentID, ID: id})
		if db.IsNoRows(err) || (err == nil && c.AuthorID != p.UserID && !mod) {
			return apperr.NotFound("comment.not_found", "No such comment.")
		}
		if err != nil {
			return err
		}
		return s.setStatus(ctx, tx, q, p, c, CommentDeleted, "comment.delete")
	})
}

// ReportComment flags a comment for moderators; enough distinct reports hide it until a
// moderator decides.
func (s *Service) ReportComment(ctx context.Context, p auth.Principal, contentID, id int64, reason string, note *string) error {
	switch reason {
	case "SPAM", "ABUSE", "OFF_TOPIC", "OTHER":
	default:
		return apperr.ValidationFailed(map[string]string{"reason": "reason must be SPAM, ABUSE, OFF_TOPIC or OTHER."})
	}
	if note != nil && len([]rune(*note)) > 500 {
		return apperr.ValidationFailed(map[string]string{"note": "Note is at most 500 characters."})
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		c, err := q.GetCommentForUpdate(ctx, engagementdb.GetCommentForUpdateParams{ContentID: contentID, ID: id})
		if db.IsNoRows(err) || (err == nil && CommentStatus(c.Status) != CommentVisible) {
			return apperr.NotFound("comment.not_found", "No such comment.")
		}
		if err != nil {
			return err
		}
		if c.AuthorID == p.UserID {
			return apperr.Invalid("comment.self_report", "You cannot report your own comment.")
		}
		n, err := q.InsertCommentReport(ctx, engagementdb.InsertCommentReportParams{
			ContentID: contentID, CommentID: id, ReporterID: p.UserID, Reason: reason, Note: note,
		})
		if err != nil || n == 0 {
			return err // already reported by this reader: nothing changes
		}
		open, err := q.CountOpenReports(ctx, id)
		if err != nil {
			return err
		}
		if open >= autoHideReports {
			return s.setStatus(ctx, tx, q, auth.Principal{}, c, CommentHidden, "comment.auto_hide")
		}
		return nil
	})
}

type ModerationAction string

const (
	ModerateHide    ModerationAction = "HIDE"
	ModerateRestore ModerationAction = "RESTORE"
	ModerateDelete  ModerationAction = "DELETE"
	ModerateDismiss ModerationAction = "DISMISS" // reports were unfounded; keep the comment visible
)

// Moderate applies a moderator's decision and closes the comment's open reports.
func (s *Service) Moderate(ctx context.Context, p auth.Principal, contentID, id int64, action ModerationAction) error {
	if err := s.requireModerator(ctx, p); err != nil {
		return err
	}
	target := map[ModerationAction]CommentStatus{
		ModerateHide: CommentHidden, ModerateRestore: CommentVisible, ModerateDelete: CommentDeleted, ModerateDismiss: CommentVisible,
	}
	status, ok := target[action]
	if !ok {
		return apperr.ValidationFailed(map[string]string{"action": "action must be HIDE, RESTORE, DELETE or DISMISS."})
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		c, err := q.GetCommentForUpdate(ctx, engagementdb.GetCommentForUpdateParams{ContentID: contentID, ID: id})
		if db.IsNoRows(err) {
			return apperr.NotFound("comment.not_found", "No such comment.")
		}
		if err != nil {
			return err
		}
		if err := s.setStatus(ctx, tx, q, p, c, status, "comment.moderate."+strings.ToLower(string(action))); err != nil {
			return err
		}
		return q.ResolveReports(ctx, engagementdb.ResolveReportsParams{CommentID: id, Resolver: &p.UserID})
	})
}

// setStatus moves a comment between states, keeping the item's comment count and the thread's
// reply count equal to what readers can actually see.
func (s *Service) setStatus(ctx context.Context, tx pgx.Tx, q *engagementdb.Queries, actor auth.Principal, c engagementdb.Comment, to CommentStatus, action string) error {
	from := CommentStatus(c.Status)
	if from == to {
		return nil
	}
	if err := q.SetCommentStatus(ctx, engagementdb.SetCommentStatusParams{ContentID: c.ContentID, ID: c.ID, Status: string(to)}); err != nil {
		return err
	}
	var delta int64
	switch {
	case from == CommentVisible:
		delta = -1
	case to == CommentVisible:
		delta = 1
	}
	if delta != 0 {
		if err := applyDeltas(ctx, q, c.ContentID, deltas{comments: delta}); err != nil {
			return err
		}
		if c.RootID != nil {
			if err := q.BumpReplyCount(ctx, engagementdb.BumpReplyCountParams{ContentID: c.ContentID, ID: *c.RootID, Delta: int32(delta)}); err != nil {
				return err
			}
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{ActorID: actor.UserID, Action: action, Entity: "comment", EntityID: c.ID,
		Data: map[string]any{"contentId": c.ContentID, "from": from, "to": to}}); err != nil {
		return err
	}
	return outbox.Publish(ctx, tx, outbox.TopicCommentsChanged, c.ContentID, outbox.CommentsEvent{ContentID: c.ContentID})
}

func (s *Service) lockOwnComment(ctx context.Context, q *engagementdb.Queries, p auth.Principal, contentID, id int64) (engagementdb.Comment, error) {
	c, err := q.GetCommentForUpdate(ctx, engagementdb.GetCommentForUpdateParams{ContentID: contentID, ID: id})
	if db.IsNoRows(err) || (err == nil && c.AuthorID != p.UserID) {
		return c, apperr.NotFound("comment.not_found", "No such comment.")
	}
	return c, err
}

// Moderators are editors with site-wide scope (comments are not tied to one section's staff).
func (s *Service) isModerator(ctx context.Context, p auth.Principal) (bool, error) {
	return s.policy.Has(ctx, p, access.RoleEditor, nil)
}

func (s *Service) requireModerator(ctx context.Context, p auth.Principal) error {
	return s.policy.Require(ctx, p, access.RoleEditor, nil)
}

// Thread lists top-level comments of a live item, newest first.
func (s *Service) Thread(ctx context.Context, contentID int64, req page.Request[CommentCursor]) (page.Page[Comment], error) {
	if err := s.requireLive(ctx, contentID); err != nil {
		return page.Page[Comment]{}, err
	}
	params := engagementdb.ListRootCommentsParams{ContentID: contentID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := engagementdb.New(s.db.Replica).ListRootComments(ctx, params)
	if err != nil {
		return page.Page[Comment]{}, err
	}
	out, err := s.hydrate(ctx, rows)
	if err != nil {
		return page.Page[Comment]{}, err
	}
	return page.Build(out, req.Limit, commentCursor), nil
}

// Replies lists one thread's replies, oldest first (conversation order).
func (s *Service) Replies(ctx context.Context, contentID, rootID int64, req page.Request[CommentCursor]) (page.Page[Comment], error) {
	params := engagementdb.ListRepliesParams{ContentID: contentID, RootID: &rootID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := engagementdb.New(s.db.Replica).ListReplies(ctx, params)
	if err != nil {
		return page.Page[Comment]{}, err
	}
	out, err := s.hydrate(ctx, rows)
	if err != nil {
		return page.Page[Comment]{}, err
	}
	return page.Build(out, req.Limit, commentCursor), nil
}

func (s *Service) hydrate(ctx context.Context, rows []engagementdb.Comment) ([]Comment, error) {
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.AuthorID)
	}
	authors, err := s.authors.PublicProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Comment, len(rows))
	for i, r := range rows {
		c := Comment{ID: r.ID, ContentID: r.ContentID, RootID: r.RootID, ParentID: r.ParentID,
			ReplyCount: r.ReplyCount, CreatedAt: r.CreatedAt, EditedAt: r.EditedAt}
		if CommentStatus(r.Status) == CommentVisible {
			a := authors[r.AuthorID]
			c.Author, c.Body = &a, r.BodyMd
		} else {
			c.Deleted = true
		}
		out[i] = c
	}
	return out, nil
}

// Report is an open report shown in the moderation queue.
type Report struct {
	ID            int64     `json:"id"`
	ContentID     int64     `json:"contentId"`
	CommentID     int64     `json:"commentId"`
	ReporterID    int64     `json:"reporterId"`
	Reason        string    `json:"reason"`
	Note          *string   `json:"note"`
	CommentBody   string    `json:"commentBody"`
	CommentAuthor int64     `json:"commentAuthorId"`
	CommentStatus string    `json:"commentStatus"`
	CreatedAt     time.Time `json:"createdAt"`
}

type ReportCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// Reports lists open reports, oldest first.
func (s *Service) Reports(ctx context.Context, p auth.Principal, req page.Request[ReportCursor]) (page.Page[Report], error) {
	if err := s.requireModerator(ctx, p); err != nil {
		return page.Page[Report]{}, err
	}
	params := engagementdb.ListOpenReportsParams{RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := engagementdb.New(s.db.Primary).ListOpenReports(ctx, params)
	if err != nil {
		return page.Page[Report]{}, err
	}
	out := make([]Report, len(rows))
	for i, r := range rows {
		out[i] = Report{ID: r.ID, ContentID: r.ContentID, CommentID: r.CommentID, ReporterID: r.ReporterID, Reason: r.Reason,
			Note: r.Note, CommentBody: r.BodyMd, CommentAuthor: r.AuthorID, CommentStatus: r.CommentStatus, CreatedAt: r.CreatedAt}
	}
	return page.Build(out, req.Limit, func(r Report) ReportCursor { return ReportCursor{At: r.CreatedAt, ID: r.ID} }), nil
}
