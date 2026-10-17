// Package problem owns practice problems: their judging setup (limits, checker, test sets),
// what the statement page shows besides the body (samples, starter code, hints), the problem
// of the day, and problem listings. The statement itself is a content item of type PROBLEM
// and goes through the normal editorial workflow.
package problem

import (
	"time"

	"code-sprout-cms/internal/content"
)

type Difficulty string

const (
	Easy   Difficulty = "EASY"
	Medium Difficulty = "MEDIUM"
	Hard   Difficulty = "HARD"
)

func (d Difficulty) valid() bool { return d == Easy || d == Medium || d == Hard }

const (
	maxSamples      = 10
	maxHints        = 10
	maxSampleBytes  = 16 << 10
	maxHintBytes    = 8 << 10
	maxTemplateCode = 64 << 10
	// UserCodePlaceholder marks where a driver template splices in the submitted code.
	UserCodePlaceholder = "{{USER_CODE}}"
)

type CreateInput struct {
	content.DraftInput
	Difficulty  Difficulty `json:"difficulty"`
	TimeLimitMs int        `json:"timeLimitMs"`
	MemoryMB    int        `json:"memoryMb"`
}

type Settings struct {
	Difficulty         Difficulty `json:"difficulty"`
	TimeLimitMs        int        `json:"timeLimitMs"`
	MemoryMB           int        `json:"memoryMb"`
	Checker            string     `json:"checker"`
	CheckerFileID      *int64     `json:"checkerFileId"`
	EditorialContentID *int64     `json:"editorialContentId"`
}

type Sample struct {
	Input       string  `json:"input"`
	Output      string  `json:"output"`
	Explanation *string `json:"explanation"`
}

type Template struct {
	Language    string  `json:"language"`
	StarterCode string  `json:"starterCode"`
	DriverCode  *string `json:"driverCode,omitempty"` // studio only; never sent to readers
}

type TestSet struct {
	ID        int64     `json:"id"`
	Version   int32     `json:"version"`
	FileID    int64     `json:"fileId"`
	CaseCount int32     `json:"caseCount"`
	Bytes     int64     `json:"bytes"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"createdAt"`
}

// StudioView is everything an author edits besides the statement.
type StudioView struct {
	ContentID int64      `json:"contentId"`
	Settings  Settings   `json:"settings"`
	Samples   []Sample   `json:"samples"`
	Templates []Template `json:"templates"`
	Hints     []string   `json:"hints"`
	TestSets  []TestSet  `json:"testSets"`
}

// PublicTemplate is starter code as readers see it (no driver).
type PublicTemplate struct {
	Language    string `json:"language"`
	StarterCode string `json:"starterCode"`
}

// Detail is the problem part of a statement page.
type Detail struct {
	Difficulty      Difficulty       `json:"difficulty"`
	TimeLimitMs     int32            `json:"timeLimitMs"`
	MemoryMB        int32            `json:"memoryMb"`
	Samples         []Sample         `json:"samples"`
	Templates       []PublicTemplate `json:"templates"`
	HintCount       int              `json:"hintCount"`
	EditorialID     *int64           `json:"editorialContentId"`
	SubmissionCount int64            `json:"submissionCount"`
	AcceptedCount   int64            `json:"acceptedCount"`
	// AcceptanceRate is accepted/submitted (0..1); nil until there are submissions.
	AcceptanceRate *float64 `json:"acceptanceRate"`
}

type Page struct {
	content.PublicPage
	Problem Detail `json:"problem"`
}

type ListItem struct {
	content.ListItem
	Difficulty     Difficulty `json:"difficulty"`
	AcceptanceRate *float64   `json:"acceptanceRate"`
}

type ListCursor struct {
	ListedAt time.Time `json:"l"`
	ID       int64     `json:"i"`
}

// JudgeSpec is everything the submission runner needs to judge one problem.
type JudgeSpec struct {
	ProblemID   int64
	TimeLimitMs int
	MemoryMB    int
	Samples     []Sample
	Drivers     map[string]string // language → driver code (absent = run code as-is)
	TestSet     *TestSetRef
}

type TestSetRef struct {
	ID     int64
	FileID int64
	SHA256 []byte
	Cases  int32
}

func acceptance(submitted, accepted int64) *float64 {
	if submitted == 0 {
		return nil
	}
	r := float64(accepted) / float64(submitted)
	return &r
}
