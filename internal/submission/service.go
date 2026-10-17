// Package submission judges code: free-form runs (article snippets, a problem's samples or
// custom input) and graded submissions against a problem's hidden test set. It also keeps
// each reader's progress (solved problems, daily activity, streaks).
//
// Runs are interactive: the job goes straight to the judge and the reader polls for the
// result. Submissions are queued and judged by the worker (runner.go), which splits large test
// sets across judge jobs, so the API never waits on the judge.
package submission

import (
	"context"
	"io"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/langs"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/submission/submissiondb"
)

// Judge is the subset of the ls-judge client used here (an interface so tests use a fake).
type Judge interface {
	Execute(ctx context.Context, req judge.ExecuteRequest, idempotencyKey string) (string, error)
	ExecuteBatch(ctx context.Context, reqs []judge.ExecuteRequest, idempotencyKey string) ([]string, error)
	Get(ctx context.Context, jobID string) (judge.JobResult, error)
}

// Problems supplies judging setups (implemented by problem.Service).
type Problems interface {
	Judging(ctx context.Context, id int64) (problem.JudgeSpec, error)
	OpenTestSet(ctx context.Context, ref problem.TestSetRef) (io.ReadCloser, error)
}

// ContentGate tells which items are live (implemented by content.Reader).
type ContentGate interface {
	LiveIDs(ctx context.Context, ids []int64) ([]int64, error)
}

// ProblemDifficulties resolves difficulty for solved-problem stats.
type ProblemDifficulties interface {
	Difficulties(ctx context.Context, ids []int64) (map[int64]problem.Difficulty, error)
}

type Service struct {
	db       *db.DB
	rdb      redis.UniversalClient
	judge    Judge
	problems Problems
	diffs    ProblemDifficulties
	content  ContentGate
	cases    *caseCache
}

// testSetCacheBytes bounds the in-memory test-set cache per worker process.
const testSetCacheBytes = 512 << 20

func NewService(d *db.DB, rdb redis.UniversalClient, j Judge, problems Problems, diffs ProblemDifficulties, gate ContentGate) *Service {
	return &Service{db: d, rdb: rdb, judge: j, problems: problems, diffs: diffs, content: gate, cases: newCaseCache(testSetCacheBytes)}
}

type Status string

const (
	StatusQueued  Status = "QUEUED"
	StatusRunning Status = "RUNNING"
	StatusDone    Status = "DONE"   // judged; Verdict says how it went
	StatusFailed  Status = "FAILED" // could not be judged (system problem, not the code's fault)
)

type Submission struct {
	ID          int64          `json:"id"`
	ProblemID   int64          `json:"problemId"`
	Language    string         `json:"language"`
	Code        *string        `json:"code,omitempty"` // only on single fetch
	Status      Status         `json:"status"`
	Verdict     *judge.Verdict `json:"verdict"`
	Passed      int32          `json:"passed"`
	Total       int32          `json:"total"`
	TimeMs      *int32         `json:"timeMs"`
	FailedIndex *int32         `json:"failedIndex,omitempty"` // 0-based; only when it is a sample case
	Message     *string        `json:"message,omitempty"`     // compiler output etc.
	CreatedAt   time.Time      `json:"createdAt"`
	FinishedAt  *time.Time     `json:"finishedAt"`
}

func fromRow(r submissiondb.Submission, withCode bool) Submission {
	s := Submission{
		ID: r.ID, ProblemID: r.ProblemID, Language: r.Language, Status: Status(r.Status), Passed: r.Passed, Total: r.Total,
		TimeMs: r.TimeMs, FailedIndex: r.FailedIndex, Message: r.Message, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
	if r.Verdict != nil {
		v := judge.Verdict(*r.Verdict)
		s.Verdict = &v
	}
	if withCode {
		s.Code = &r.Code
	}
	return s
}

type SubmitInput struct {
	ProblemID int64  `json:"problemId"`
	Language  string `json:"language"`
	Code      string `json:"code"`
}

func validateCode(language, code string) error {
	f := map[string]string{}
	if !langs.Runnable(language) {
		f["language"] = "Unsupported language; use one of " + langs.List() + "."
	}
	if len(code) == 0 || len(code) > judge.MaxCodeBytes {
		f["code"] = "Code is required and at most 64 KiB."
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	return nil
}

func (s *Service) requireLiveProblem(ctx context.Context, id int64) (problem.JudgeSpec, error) {
	notFound := apperr.NotFound("problem.not_found", "No such problem.")
	live, err := s.content.LiveIDs(ctx, []int64{id})
	if err != nil {
		return problem.JudgeSpec{}, err
	}
	if len(live) == 0 {
		return problem.JudgeSpec{}, notFound
	}
	spec, err := s.problems.Judging(ctx, id)
	if e, ok := apperr.As(err); ok && e.Kind == apperr.KindNotFound {
		return problem.JudgeSpec{}, notFound
	}
	return spec, err
}

// Submit queues code for judging against the hidden tests; poll Get until Status is DONE.
func (s *Service) Submit(ctx context.Context, p auth.Principal, in SubmitInput) (Submission, error) {
	if err := validateCode(in.Language, in.Code); err != nil {
		return Submission{}, err
	}
	spec, err := s.requireLiveProblem(ctx, in.ProblemID)
	if err != nil {
		return Submission{}, err
	}
	if spec.TestSet == nil {
		return Submission{}, apperr.Conflict("problem.not_judgeable", "This problem has no tests yet.")
	}
	row, err := submissiondb.New(s.db.Primary).InsertSubmission(ctx, submissiondb.InsertSubmissionParams{
		UserID: p.UserID, ProblemID: in.ProblemID, Language: in.Language, Code: in.Code,
	})
	if err != nil {
		return Submission{}, err
	}
	return fromRow(row, false), nil
}

func (s *Service) Get(ctx context.Context, p auth.Principal, id int64) (Submission, error) {
	row, err := submissiondb.New(s.db.Primary).GetSubmission(ctx, submissiondb.GetSubmissionParams{UserID: p.UserID, ID: id})
	if db.IsNoRows(err) {
		return Submission{}, apperr.NotFound("submission.not_found", "No such submission.")
	}
	if err != nil {
		return Submission{}, err
	}
	return fromRow(row, true), nil
}

type SubmissionCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

func (s *Service) List(ctx context.Context, p auth.Principal, problemID *int64, req page.Request[SubmissionCursor]) (page.Page[Submission], error) {
	params := submissiondb.ListUserSubmissionsParams{UserID: p.UserID, ProblemID: problemID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := submissiondb.New(s.db.Primary).ListUserSubmissions(ctx, params)
	if err != nil {
		return page.Page[Submission]{}, err
	}
	out := make([]Submission, len(rows))
	for i, r := range rows {
		out[i] = fromRow(submissiondb.Submission{
			ID: r.ID, UserID: r.UserID, ProblemID: r.ProblemID, Language: r.Language, Status: r.Status, Verdict: r.Verdict,
			Passed: r.Passed, Total: r.Total, TimeMs: r.TimeMs, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
		}, false)
	}
	return page.Build(out, req.Limit, func(s Submission) SubmissionCursor { return SubmissionCursor{At: s.CreatedAt, ID: s.ID} }), nil
}

type ProblemState struct {
	Attempted bool       `json:"attempted"`
	Solved    bool       `json:"solved"`
	SolvedAt  *time.Time `json:"solvedAt,omitempty"`
}

// States answers attempted/solved for up to 100 problems (problem lists show a tick).
func (s *Service) States(ctx context.Context, p auth.Principal, ids []int64) (map[int64]ProblemState, error) {
	if len(ids) > 100 {
		return nil, apperr.ValidationFailed(map[string]string{"ids": "At most 100 ids."})
	}
	out := make(map[int64]ProblemState, len(ids))
	for _, id := range ids {
		out[id] = ProblemState{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := submissiondb.New(s.db.Primary).ProblemStates(ctx, submissiondb.ProblemStatesParams{UserID: p.UserID, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ProblemID] = ProblemState{Attempted: true, Solved: r.FirstSolvedAt != nil, SolvedAt: r.FirstSolvedAt}
	}
	return out, nil
}

type Solved struct {
	ProblemID int64     `json:"problemId"`
	SolvedAt  time.Time `json:"solvedAt"`
}

type SolvedCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

func (s *Service) Solved(ctx context.Context, p auth.Principal, req page.Request[SolvedCursor]) (page.Page[Solved], error) {
	params := submissiondb.ListSolvedParams{UserID: p.UserID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterSolved, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := submissiondb.New(s.db.Primary).ListSolved(ctx, params)
	if err != nil {
		return page.Page[Solved]{}, err
	}
	out := make([]Solved, len(rows))
	for i, r := range rows {
		out[i] = Solved{ProblemID: r.ProblemID, SolvedAt: *r.FirstSolvedAt}
	}
	return page.Build(out, req.Limit, func(s Solved) SolvedCursor { return SolvedCursor{At: s.SolvedAt, ID: s.ProblemID} }), nil
}

// Stats is the profile dashboard: solved counts, streaks, a year of activity, languages.
type Stats struct {
	TotalSolved       int            `json:"totalSolved"`
	EasySolved        int            `json:"easySolved"`
	MediumSolved      int            `json:"mediumSolved"`
	HardSolved        int            `json:"hardSolved"`
	TotalSubmissions  int64          `json:"totalSubmissions"`
	CurrentStreak     int            `json:"currentStreak"` // consecutive days with a submission, ending today or yesterday
	LongestStreak     int            `json:"longestStreak"`
	Activity          map[string]int `json:"activity"`          // "YYYY-MM-DD" → submissions, last 365 days
	LanguageBreakdown map[string]int `json:"languageBreakdown"` // language → problems solved in it
}

func (s *Service) Stats(ctx context.Context, p auth.Principal) (Stats, error) {
	q := submissiondb.New(s.db.Replica)
	st := Stats{Activity: map[string]int{}, LanguageBreakdown: map[string]int{}}

	solved, err := q.SolvedProblemIDs(ctx, p.UserID)
	if err != nil {
		return st, err
	}
	st.TotalSolved = len(solved)
	diffs, err := s.diffs.Difficulties(ctx, solved)
	if err != nil {
		return st, err
	}
	for _, d := range diffs {
		switch d {
		case problem.Easy:
			st.EasySolved++
		case problem.Medium:
			st.MediumSolved++
		case problem.Hard:
			st.HardSolved++
		}
	}
	if st.TotalSubmissions, err = q.CountUserSubmissions(ctx, p.UserID); err != nil {
		return st, err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	days, err := q.ActivitySince(ctx, submissiondb.ActivitySinceParams{UserID: p.UserID, Day: today.AddDate(0, 0, -365)})
	if err != nil {
		return st, err
	}
	active := make([]time.Time, 0, len(days))
	for _, d := range days {
		st.Activity[d.Day.Format(time.DateOnly)] = int(d.Submissions)
		active = append(active, d.Day)
	}
	st.CurrentStreak, st.LongestStreak = streaks(active, today)
	langs, err := q.AcceptedByLanguage(ctx, p.UserID)
	if err != nil {
		return st, err
	}
	for _, l := range langs {
		st.LanguageBreakdown[l.Language] = int(l.Solved)
	}
	return st, nil
}

// streaks computes the current run of consecutive active days (alive if it reaches today or
// yesterday, so a streak does not break before the day is over) and the longest run.
func streaks(days []time.Time, today time.Time) (current, longest int) {
	if len(days) == 0 {
		return 0, 0
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	run := 1
	longest = 1
	for i := 1; i < len(days); i++ {
		if days[i].Sub(days[i-1]) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	last := days[len(days)-1]
	if today.Sub(last) > 24*time.Hour {
		return 0, longest
	}
	return run, longest
}
