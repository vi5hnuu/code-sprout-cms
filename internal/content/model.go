// Package content owns articles, blogs and topic pages: drafts and their revision history,
// the editorial workflow (submit → review → publish/schedule), and the public face written at
// publish. Problems and quizzes build on it from their own domains.
package content

import (
	"time"

	"code-sprout-cms/internal/markdown"
)

type Type string

const (
	TypeArticle   Type = "ARTICLE"
	TypeBlog      Type = "BLOG"
	TypeProblem   Type = "PROBLEM"
	TypeTopicPage Type = "TOPIC_PAGE"
	TypeQuiz      Type = "QUIZ"
)

// studioTypes can be created through the generic studio endpoints. Problems and quizzes carry
// extra structure (test sets, questions) and are created through their own domains.
var studioTypes = map[Type]bool{TypeArticle: true, TypeBlog: true, TypeTopicPage: true}

type Status string

const (
	StatusDraft     Status = "DRAFT"     // never published
	StatusPublished Status = "PUBLISHED" // live
	StatusArchived  Status = "ARCHIVED"  // taken down; can be republished
)

type ReviewState string

const (
	ReviewNone             ReviewState = "NONE"
	ReviewInReview         ReviewState = "IN_REVIEW"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewApproved         ReviewState = "APPROVED"
	ReviewScheduled        ReviewState = "SCHEDULED"
)

// locked: while a draft waits on a reviewer or the scheduler, only editors may change it, so
// what gets approved is what gets published.
func (r ReviewState) locked() bool {
	return r == ReviewInReview || r == ReviewApproved || r == ReviewScheduled
}

const (
	VisibilityPublic   = "PUBLIC"
	VisibilityUnlisted = "UNLISTED" // live by URL, but kept out of listings, search and sitemaps
	VisibilityPrivate  = "PRIVATE"
	AccessFree         = "FREE"
	AccessPremium      = "PREMIUM"
)

// Meta is the draft's metadata, versioned with the body in each revision. It becomes the
// public face (content row) only at publish, so editing a live page's title or slug in a draft
// never changes what readers see before it is approved.
//
// No-op saves are detected by marshalling both the stored and the incoming Meta through this
// struct (tag ids sorted and de-duplicated) and comparing the bytes.
type Meta struct {
	Title      string         `json:"title"`
	Slug       string         `json:"slug"`
	Summary    *string        `json:"summary"`
	CategoryID *int64         `json:"categoryId"`
	TagIDs     []int64        `json:"tagIds"`
	Visibility string         `json:"visibility"`
	Access     string         `json:"access"`
	Article    *ArticleFields `json:"article,omitempty"`
	Blog       *BlogFields    `json:"blog,omitempty"`
}

type ArticleFields struct {
	Level *string `json:"level"` // BEGINNER | INTERMEDIATE | ADVANCED
}

type BlogFields struct {
	CoverFileID *int64 `json:"coverFileId"`
}

// DraftInput is a full replacement of the draft (PUT semantics): what the editor shows is
// exactly what gets saved, so there is no partial-update ambiguity.
type DraftInput struct {
	Meta
	Body    string  `json:"body"`
	Message *string `json:"message"` // optional revision note ("fix typo in example")
}

type CreateInput struct {
	Type   Type   `json:"type"`
	Locale string `json:"locale"` // default "en"; fixed for the item's lifetime
	DraftInput
}

// LiveFace is what readers currently see, shown in the studio next to the draft.
type LiveFace struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	RevNo int32  `json:"revNo"`
}

// Draft is the studio view of one item.
type Draft struct {
	ID                    int64            `json:"id"`
	Type                  Type             `json:"type"`
	Locale                string           `json:"locale"`
	Status                Status           `json:"status"`
	ReviewState           ReviewState      `json:"reviewState"`
	ScheduledAt           *time.Time       `json:"scheduledAt"`
	AuthorID              int64            `json:"authorId"`
	DraftRevNo            int32            `json:"draftRevNo"` // send as If-Match when saving
	HasUnpublishedChanges bool             `json:"hasUnpublishedChanges"`
	Live                  *LiveFace        `json:"live"`
	PublishedAt           *time.Time       `json:"publishedAt"`
	LastPublishedAt       *time.Time       `json:"lastPublishedAt"`
	UpdatedAt             time.Time        `json:"updatedAt"`
	Meta                  Meta             `json:"meta"`
	Body                  string           `json:"body"`
	Issues                []markdown.Issue `json:"issues"` // must be empty to submit or publish
}

// Summary is one row of a studio listing.
type Summary struct {
	ID          int64       `json:"id"`
	Type        Type        `json:"type"`
	Locale      string      `json:"locale"`
	Title       string      `json:"title"`
	Slug        string      `json:"slug"`
	Status      Status      `json:"status"`
	ReviewState ReviewState `json:"reviewState"`
	AuthorID    int64       `json:"authorId"`
	DraftRevNo  int32       `json:"draftRevNo"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

type ListCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        int64     `json:"i"`
}

type Revision struct {
	RevNo     int32     `json:"revNo"`
	EditorID  int64     `json:"editorId"`
	Message   *string   `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
	Archived  bool      `json:"archived"` // body moved to cold storage; fetch may be slower
}

type RevisionDetail struct {
	Revision
	Meta Meta   `json:"meta"`
	Body string `json:"body"`
}

type ReviewDecision string

const (
	DecisionApproved         ReviewDecision = "APPROVED"
	DecisionChangesRequested ReviewDecision = "CHANGES_REQUESTED"
)

type ReviewNote struct {
	// Anchor locates the note in the draft, e.g. {"line": 42} or {"heading": "complexity"}.
	Anchor map[string]any `json:"anchor"`
	Body   string         `json:"body"`
}

type ReviewInput struct {
	Decision ReviewDecision `json:"decision"`
	Summary  *string        `json:"summary"`
	Notes    []ReviewNote   `json:"notes"`
}

type Review struct {
	ID         int64          `json:"id"`
	RevNo      int32          `json:"revNo"`
	ReviewerID int64          `json:"reviewerId"`
	Decision   ReviewDecision `json:"decision"`
	Summary    *string        `json:"summary"`
	Notes      []ReviewNote   `json:"notes"`
	CreatedAt  time.Time      `json:"createdAt"`
}

type ScheduleInput struct {
	At time.Time `json:"at"`
}
