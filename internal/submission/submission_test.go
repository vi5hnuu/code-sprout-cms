package submission_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/submission"
)

// fakeJudge decides each case from the submitted source: a source containing "WRONG:<s>"
// fails every case whose stdin contains <s>; "COMPILE_ERROR" fails to compile. Jobs report
// "running" on their first poll so the runner's polling is exercised.
type fakeJudge struct {
	mu        sync.Mutex
	jobs      map[string]judge.ExecuteRequest
	polled    map[string]bool
	byKey     map[string][]string
	batches   int
	failNext  error // returned by the next ExecuteBatch, then cleared
	jobFailed bool  // jobs finish as "failed" (judge-side error)
	seq       int
	sources   []string
}

func newFake() *fakeJudge {
	return &fakeJudge{jobs: map[string]judge.ExecuteRequest{}, polled: map[string]bool{}, byKey: map[string][]string{}}
}

func (f *fakeJudge) Execute(_ context.Context, req judge.ExecuteRequest, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(req), nil
}

func (f *fakeJudge) add(req judge.ExecuteRequest) string {
	f.seq++
	id := fmt.Sprintf("job-%d", f.seq)
	f.jobs[id] = req
	f.sources = append(f.sources, req.Code)
	return id
}

func (f *fakeJudge) ExecuteBatch(_ context.Context, reqs []judge.ExecuteRequest, key string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	if ids, ok := f.byKey[key]; ok {
		return ids, nil // idempotent replay
	}
	f.batches++
	ids := make([]string, len(reqs))
	for i, r := range reqs {
		ids[i] = f.add(r)
	}
	f.byKey[key] = ids
	return ids, nil
}

func (f *fakeJudge) Get(_ context.Context, id string) (judge.JobResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req, ok := f.jobs[id]
	if !ok {
		return judge.JobResult{}, judge.ErrNotFound
	}
	if !f.polled[id] {
		f.polled[id] = true
		return judge.JobResult{JobID: id, Status: judge.JobRunning}, nil
	}
	if f.jobFailed {
		return judge.JobResult{JobID: id, Status: judge.JobFailed}, nil
	}
	// Results as judge.Client returns them: 0-based cases, a compile error on its own.
	out := judge.JobResult{JobID: id, Status: judge.JobDone}
	if strings.Contains(req.Code, "COMPILE_ERROR") {
		msg := "main.py:1: SyntaxError"
		out.Compile = &judge.CaseResult{Verdict: judge.VerdictCE, Stderr: &msg}
		return out, nil
	}
	for i, c := range req.TestCases {
		v, stdout := judge.VerdictAC, c.Expected
		ms := 10 + i
		if wrongFor(req.Code, c.Stdin) {
			v, stdout = judge.VerdictWA, "nope"
		}
		out.Results = append(out.Results, judge.CaseResult{Index: i, Verdict: v, Stdout: &stdout, TimeMs: &ms})
	}
	return out, nil
}

func wrongFor(code, stdin string) bool {
	_, marker, ok := strings.Cut(code, "WRONG:")
	if !ok {
		return false
	}
	marker, _, _ = strings.Cut(marker, "\n")
	return strings.Contains(stdin, marker)
}

type fixture struct {
	*arenatest.Env
	judge *fakeJudge
	svc   *submission.Service
}

func newFixture(t *testing.T) *fixture {
	e := arenatest.New(t)
	fj := newFake()
	return &fixture{Env: e, judge: fj,
		svc: submission.NewService(e.DB, testenv.Redis(t), fj, e.Problems, e.Problems, e.Reader)}
}

func hidden(tag string, n int) []judge.TestCase {
	out := make([]judge.TestCase, n)
	for i := range out {
		out[i] = judge.TestCase{Stdin: fmt.Sprintf("%s case %d", tag, i), Expected: fmt.Sprint(i)}
	}
	return out
}

// drain runs the queue until our submission is finished (other tests may add work too).
func (f *fixture) judgeUntilDone(t *testing.T, owner auth.Principal, id int64) submission.Submission {
	t.Helper()
	ctx := context.Background()
	for range 50 {
		if _, err := f.svc.RunNext(ctx); err != nil && !errors.Is(err, judge.ErrUnavailable) && !errors.Is(err, judge.ErrBusy) {
			t.Fatal(err)
		}
		s, err := f.svc.Get(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Status == submission.StatusDone || s.Status == submission.StatusFailed {
			return s
		}
	}
	t.Fatalf("submission %d never finished", id)
	return submission.Submission{}
}

func TestSubmissionsAreJudgedInBatches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag := testenv.Unique("sum")
	// 250 hidden cases → 3 judge jobs (100+100+50) in one batch call.
	problemID := f.Publish(tag, []problem.Sample{{Input: tag + " sample", Output: "3"}}, hidden(tag, 250), nil)
	solver := testenv.User(t, f.DB)

	_, err := f.svc.Submit(ctx, solver, submission.SubmitInput{ProblemID: problemID, Language: "cobol", Code: "x"})
	if e, ok := apperr.As(err); !ok || e.Fields["language"] == "" {
		t.Fatalf("bad language accepted: %v", err)
	}
	_, err = f.svc.Submit(ctx, solver, submission.SubmitInput{ProblemID: 999999999, Language: "python", Code: "x"})
	if e, ok := apperr.As(err); !ok || e.Code != "problem.not_found" {
		t.Fatalf("unknown problem: %v", err)
	}

	// Wrong on hidden case 150: verdict WA, the failing case is NOT revealed (it is hidden).
	wrong, err := f.svc.Submit(ctx, solver, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "WRONG:case 150\n"})
	if err != nil || wrong.Status != submission.StatusQueued {
		t.Fatalf("submit = %+v, %v", wrong, err)
	}
	got := f.judgeUntilDone(t, solver, wrong.ID)
	if got.Verdict == nil || *got.Verdict != judge.VerdictWA || got.Passed != 249 || got.Total != 250 || got.FailedIndex != nil {
		t.Fatalf("WA result = %+v verdict=%v", got, got.Verdict)
	}
	if f.judge.batches != 1 || len(f.judge.jobs) != 3 {
		t.Fatalf("batches=%d jobs=%d, want 1 batch of 3 jobs", f.judge.batches, len(f.judge.jobs))
	}

	ok, _ := f.svc.Submit(ctx, solver, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "print(sum)\n"})
	got = f.judgeUntilDone(t, solver, ok.ID)
	if *got.Verdict != judge.VerdictAC || got.Passed != 250 || got.TimeMs == nil {
		t.Fatalf("AC result = %+v", got)
	}

	ce, _ := f.svc.Submit(ctx, solver, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "COMPILE_ERROR"})
	got = f.judgeUntilDone(t, solver, ce.ID)
	if *got.Verdict != judge.VerdictCE || got.Passed != 0 || got.Message == nil || !strings.Contains(*got.Message, "SyntaxError") {
		t.Fatalf("CE result = %+v", got)
	}

	st, err := f.svc.States(ctx, solver, []int64{problemID})
	if err != nil || !st[problemID].Solved {
		t.Fatalf("state = %+v, %v", st, err)
	}
	stats, err := f.svc.Stats(ctx, solver)
	if err != nil || stats.TotalSolved != 1 || stats.EasySolved != 1 || stats.TotalSubmissions != 3 || stats.CurrentStreak != 1 ||
		stats.LanguageBreakdown["python"] != 1 || stats.Activity[time.Now().UTC().Format(time.DateOnly)] != 3 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	var subs, accepted int64
	if err := f.DB.Primary.QueryRow(ctx, "SELECT submission_count, accepted_count FROM problem_ext WHERE content_id=$1", problemID).Scan(&subs, &accepted); err != nil {
		t.Fatal(err)
	}
	if subs != 3 || accepted != 1 {
		t.Fatalf("problem counters %d/%d, want 3/1", subs, accepted)
	}

	list, err := f.svc.List(ctx, solver, &problemID, page.Request[submission.SubmissionCursor]{Limit: 10})
	if err != nil || len(list.Items) != 3 || list.Items[0].ID != ce.ID || list.Items[0].Code != nil {
		t.Fatalf("history = %+v, %v", list.Items, err)
	}
	other := testenv.User(t, f.DB)
	if _, err := f.svc.Get(ctx, other, ok.ID); err == nil {
		t.Fatal("another user read a submission")
	}
}

func TestFailingSampleIsRevealed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag := testenv.Unique("smp")
	// Samples are judged with the hidden set only if they are in it; here case 0 is visible.
	problemID := f.Publish(tag, []problem.Sample{{Input: tag + " case 0", Output: "0"}}, hidden(tag, 5), nil)
	u := testenv.User(t, f.DB)
	s, _ := f.svc.Submit(ctx, u, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "WRONG:case 0\n"})
	got := f.judgeUntilDone(t, u, s.ID)
	if got.FailedIndex == nil || *got.FailedIndex != 0 {
		t.Fatalf("failing sample should be pointed out: %+v", got)
	}
}

func TestDriverWrapsSubmittedCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag := testenv.Unique("drv")
	driver := "import sys\n{{USER_CODE}}\nprint(solve())\n"
	problemID := f.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, hidden(tag, 3), &driver)
	u := testenv.User(t, f.DB)
	s, _ := f.svc.Submit(ctx, u, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "def solve(): return 1"})
	f.judgeUntilDone(t, u, s.ID)
	want := "import sys\ndef solve(): return 1\nprint(solve())\n"
	for _, src := range f.judge.sources {
		if src == want {
			return
		}
	}
	t.Fatalf("judge never saw the spliced source; saw %q", f.judge.sources)
}

func TestTransientJudgeFailuresRetry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag := testenv.Unique("tr")
	problemID := f.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, hidden(tag, 3), nil)
	u := testenv.User(t, f.DB)
	s, _ := f.svc.Submit(ctx, u, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "ok"})

	f.judge.failNext = fmt.Errorf("%w: down", judge.ErrUnavailable)
	if _, err := f.svc.RunNext(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := f.svc.Get(ctx, u, s.ID)
	if got.Status != submission.StatusQueued {
		t.Fatalf("transient failure should requeue: %s", got.Status)
	}
	// Requeued work waits out its retry delay; make it due now.
	if _, err := f.DB.Primary.Exec(ctx, "UPDATE submission SET lease_until = now() WHERE id=$1", s.ID); err != nil {
		t.Fatal(err)
	}
	if got = f.judgeUntilDone(t, u, s.ID); *got.Verdict != judge.VerdictAC {
		t.Fatalf("retry result = %+v", got)
	}

	// A judge that keeps failing jobs internally eventually gives up with FAILED, never a verdict.
	f.judge.jobFailed = true
	s2, _ := f.svc.Submit(ctx, u, submission.SubmitInput{ProblemID: problemID, Language: "python", Code: "ok again"})
	for range 20 {
		if _, err := f.DB.Primary.Exec(ctx, "UPDATE submission SET lease_until = now() WHERE id=$1 AND status='QUEUED'", s2.ID); err != nil {
			t.Fatal(err)
		}
		_, _ = f.svc.RunNext(ctx)
		if got, _ = f.svc.Get(ctx, u, s2.ID); got.Status == submission.StatusFailed {
			break
		}
	}
	if got.Status != submission.StatusFailed || got.Verdict != nil {
		t.Fatalf("persistent judge errors: %+v", got)
	}
}

func TestRunsBelongToTheirOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tag := testenv.Unique("run")
	problemID := f.Publish(tag, []problem.Sample{{Input: "1 2", Output: "3"}, {Input: "2 2", Output: "4"}}, hidden(tag, 2), nil)

	ticket, err := f.svc.RunSnippet(ctx, "ip1", submission.SnippetInput{Language: "python", Code: "print(input())", Stdin: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RunResult(ctx, "ip2", ticket.RunID); err == nil {
		t.Fatal("another client read the run")
	}
	var res submission.RunResult
	for range 3 {
		if res, err = f.svc.RunResult(ctx, "ip1", ticket.RunID); err != nil {
			t.Fatal(err)
		}
		if res.Done {
			break
		}
	}
	if !res.Done || len(res.Cases) != 1 || res.Cases[0].Verdict != "OK" {
		t.Fatalf("snippet run = %+v", res)
	}

	samples, err := f.svc.RunProblem(ctx, "u1", problemID, submission.ProblemRunInput{Language: "python", Code: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 { // first poll reports "running"
		if res, err = f.svc.RunResult(ctx, "u1", samples.RunID); err != nil || res.Done {
			break
		}
	}
	if len(res.Cases) != 2 || res.Cases[0].Verdict != "AC" {
		t.Fatalf("sample run = %+v", res)
	}
}
