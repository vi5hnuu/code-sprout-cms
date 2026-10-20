package submission

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/text"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/submission/submissiondb"
)

const (
	// leaseFor must outlast one full judging pass (dispatch + polling deadline).
	leaseFor     = 10 * time.Minute
	pollDeadline = 8 * time.Minute
	maxAttempts  = 8
	// Transient judge trouble (down, quota, rate limit) retries after this delay.
	retryAfter = 30 * time.Second
)

// RunNext judges one waiting submission. It reports whether there was one, so the worker
// loops while the queue has work.
func (s *Service) RunNext(ctx context.Context) (bool, error) {
	q := submissiondb.New(s.db.Primary)
	sub, err := q.LeaseSubmission(ctx, int32(leaseFor.Seconds()))
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A runner that died mid-judging (crash, OOM) leaves the lease to expire and the
	// submission is leased again; past maxAttempts it is failed instead of looping forever.
	if sub.Attempts > maxAttempts {
		return true, s.fail(ctx, sub, "We could not judge this submission. Please submit again.")
	}
	if err := s.judgeSubmission(ctx, sub); err != nil {
		return true, s.handleRunError(ctx, sub, err)
	}
	return true, nil
}

// RunQueue processes submissions until ctx ends; several workers can run it at once.
func (s *Service) RunQueue(ctx context.Context, idle time.Duration) {
	for {
		worked, err := s.RunNext(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("submission run failed", "err", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(idle):
		}
	}
}

// outcome is a judged result, ready to store.
type outcome struct {
	verdict     judge.Verdict
	passed      int
	total       int
	timeMs      *int32
	failedIndex *int32
	message     *string
}

func (s *Service) judgeSubmission(ctx context.Context, sub submissiondb.Submission) error {
	spec, err := s.problems.Judging(ctx, sub.ProblemID)
	if err != nil {
		return err
	}
	if spec.TestSet == nil {
		return s.fail(ctx, sub, "The problem has no tests.")
	}
	cases, err := s.loadCases(ctx, *spec.TestSet)
	if err != nil {
		return err
	}

	// The plan is a pure function of the cases and the code, so a resumed submission maps its
	// recorded jobs back to the same case ranges.
	source := SourceFor(spec, sub.Language, sub.Code)
	spans, err := judge.PlanJobs(cases, source)
	if errors.Is(err, judge.ErrCaseTooLarge) {
		return s.fail(ctx, sub, "A test case of this problem is too large to judge. Please report the problem.")
	}
	if err != nil {
		return err
	}
	jobs := sub.JudgeJobs
	// Jobs recorded under a different plan, or against a test set that has since been replaced,
	// cannot be mapped to these cases: judge afresh.
	if len(jobs) != len(spans) || sub.TestSetID == nil || *sub.TestSetID != spec.TestSet.ID {
		jobs = nil
	}
	if len(jobs) == 0 {
		if jobs, err = s.dispatch(ctx, sub, source, spec, cases, spans); err != nil {
			return err
		}
		if err := submissiondb.New(s.db.Primary).SetSubmissionJobs(ctx, submissiondb.SetSubmissionJobsParams{
			UserID: sub.UserID, ID: sub.ID, Jobs: jobs, TestSetID: &spec.TestSet.ID,
		}); err != nil {
			return err
		}
	}

	results, err := s.await(ctx, jobs)
	if err != nil {
		return err
	}
	out := aggregate(results, spans, len(cases), len(spec.Samples))
	if out.verdict == judge.VerdictIE {
		// Forget these jobs so the retry dispatches fresh ones rather than re-reading failures.
		if err := submissiondb.New(s.db.Primary).SetSubmissionJobs(ctx, submissiondb.SetSubmissionJobsParams{
			UserID: sub.UserID, ID: sub.ID, Jobs: []string{}, TestSetID: &spec.TestSet.ID,
		}); err != nil {
			return err
		}
		return fmt.Errorf("%w: judge reported an internal error", judge.ErrUnavailable)
	}
	return s.finish(ctx, sub, out)
}

// SourceFor applies the problem's driver for the language, if it has one (function-style
// problems); otherwise the code runs as submitted.
func SourceFor(spec problem.JudgeSpec, language, code string) string {
	if driver, ok := spec.Drivers[language]; ok {
		return strings.Replace(driver, problem.UserCodePlaceholder, code, 1)
	}
	return code
}

// dispatch sends the cases as judge jobs planned by judge.PlanJobs (by case count and encoded
// size), in /batch/execute calls planned by judge.PlanBatches. Idempotency keys cover the
// submission, attempt and batch: a failed batch call retried within the attempt cannot create
// duplicate jobs, while a new attempt (after a judge error) gets fresh jobs. A runner that
// crashes between dispatching and recording the job ids re-runs the tests on its next
// attempt: wasted work, never a wrong verdict.
func (s *Service) dispatch(ctx context.Context, sub submissiondb.Submission, source string, spec problem.JudgeSpec, cases []judge.TestCase, spans []judge.Span) ([]string, error) {
	reqs := make([]judge.ExecuteRequest, len(spans))
	sizes := make([]int, len(spans))
	for i, sp := range spans {
		reqs[i] = judge.ExecuteRequest{
			Language: sub.Language, Code: source, TimeLimitMs: spec.TimeLimitMs, MemLimitMB: spec.MemoryMB, TestCases: cases[sp.Start:sp.End],
		}
		sizes[i] = judge.JobSize(cases, source, sp)
	}
	var jobs []string
	for b, batch := range judge.PlanBatches(sizes) {
		key := fmt.Sprintf("submission-%d-%d-attempt-%d-batch-%d", sub.UserID, sub.ID, sub.Attempts, b)
		ids, err := s.judge.ExecuteBatch(ctx, reqs[batch.Start:batch.End], key)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, ids...)
	}
	return jobs, nil
}

// await polls the jobs until all finish or the deadline passes. The judge serves finished
// jobs from its Redis cache, so polling is cheap; the interval backs off to 2s.
func (s *Service) await(ctx context.Context, jobs []string) ([]judge.JobResult, error) {
	ctx, cancel := context.WithTimeout(ctx, pollDeadline)
	defer cancel()
	results := make([]judge.JobResult, len(jobs))
	done := make([]bool, len(jobs))
	interval := 300 * time.Millisecond
	for {
		remaining := 0
		for i, id := range jobs {
			if done[i] {
				continue
			}
			r, err := s.judge.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			if r.Status.Finished() {
				results[i], done[i] = r, true
				continue
			}
			remaining++
		}
		if remaining == 0 {
			return results, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: judging did not finish in time", judge.ErrUnavailable)
		case <-time.After(interval):
		}
		interval = min(interval*2, 2*time.Second)
	}
}

// aggregate folds per-job results into one verdict, in case order: the first case that did not
// pass decides the verdict, the slowest case gives the time. Only a failing *sample* is pointed
// out to the user; hidden cases stay hidden.
func aggregate(jobs []judge.JobResult, spans []judge.Span, total, samples int) outcome {
	out := outcome{verdict: judge.VerdictAC, total: total}
	var slowest *int
	for n, j := range jobs {
		if j.Status == judge.JobFailed {
			return outcome{verdict: judge.VerdictIE, total: total}
		}
		// The same code compiles the same way in every job: one compile error decides.
		if j.Compile != nil {
			ce := outcome{verdict: judge.VerdictCE, total: total}
			if j.Compile.Stderr != nil {
				msg := text.Clip(*j.Compile.Stderr, 4000)
				ce.message = &msg
			}
			return ce
		}
		offset := spans[n].Start
		for _, c := range j.Results {
			idx := offset + c.Index
			if c.TimeMs != nil && (slowest == nil || *c.TimeMs > *slowest) {
				slowest = c.TimeMs
			}
			switch {
			case c.Verdict == judge.VerdictAC:
				out.passed++
			case c.Verdict == judge.VerdictSK:
				// Not run: an earlier case in this job already decided the verdict.
			case out.verdict == judge.VerdictAC:
				out.verdict = c.Verdict
				if idx < samples {
					i := int32(idx)
					out.failedIndex = &i
				}
				if c.Verdict == judge.VerdictCE && c.Stderr != nil {
					msg := text.Clip(*c.Stderr, 4000)
					out.message = &msg
				}
			}
		}
	}
	if out.verdict == judge.VerdictCE {
		out.passed = 0
	}
	if slowest != nil {
		t := int32(*slowest)
		out.timeMs = &t
	}
	return out
}

// finish stores the verdict and moves the reader's progress and the problem's counters in one
// transaction, so a solved problem and its counts always agree.
func (s *Service) finish(ctx context.Context, sub submissiondb.Submission, out outcome) error {
	accepted := out.verdict == judge.VerdictAC
	v := string(out.verdict)
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := submissiondb.New(tx)
		n, err := q.FinishSubmission(ctx, submissiondb.FinishSubmissionParams{
			UserID: sub.UserID, ID: sub.ID, Status: string(StatusDone), Verdict: &v, Passed: int32(out.passed),
			Total: int32(out.total), TimeMs: out.timeMs, FailedIndex: out.failedIndex, Message: out.message,
		})
		if err != nil || n == 0 {
			return err // no row: the reader erased their account; record no progress for them
		}
		if err := q.UpsertUserProblem(ctx, submissiondb.UpsertUserProblemParams{UserID: sub.UserID, ProblemID: sub.ProblemID, Solved: accepted}); err != nil {
			return err
		}
		if err := scheduleReview(ctx, q, sub.UserID, sub.ProblemID, accepted, time.Now()); err != nil {
			return err
		}
		var a int32
		if accepted {
			a = 1
		}
		if err := q.AddActivity(ctx, submissiondb.AddActivityParams{UserID: sub.UserID, Accepted: a}); err != nil {
			return err
		}
		return problem.RecordResult(ctx, tx, sub.ProblemID, accepted)
	})
}

// fail marks a submission as not judgeable (a system problem, not the user's code).
func (s *Service) fail(ctx context.Context, sub submissiondb.Submission, reason string) error {
	_, err := submissiondb.New(s.db.Primary).FinishSubmission(ctx, submissiondb.FinishSubmissionParams{
		UserID: sub.UserID, ID: sub.ID, Status: string(StatusFailed), Message: &reason,
	})
	return err
}

// handleRunError retries transient judge trouble later and gives up after maxAttempts.
func (s *Service) handleRunError(ctx context.Context, sub submissiondb.Submission, err error) error {
	// Shutting down: leave the lease to expire so another runner picks the submission up.
	if ctx.Err() != nil {
		return err
	}
	// Only a refusal by the judge is final. Anything else (judge down or busy, a database or
	// storage hiccup) is retried until the attempts run out.
	if !errors.Is(err, judge.ErrRejected) && sub.Attempts < maxAttempts {
		slog.Warn("submission requeued", "submission", sub.ID, "attempt", sub.Attempts, "err", err)
		return submissiondb.New(s.db.Primary).RequeueSubmission(ctx, submissiondb.RequeueSubmissionParams{
			UserID: sub.UserID, ID: sub.ID, RetryAt: ptrTime(time.Now().Add(retryAfter)),
		})
	}
	reason := "We could not judge this submission. Please submit again."
	if errors.Is(err, judge.ErrRejected) {
		reason = "The judge refused this submission: " + strings.TrimPrefix(err.Error(), judge.ErrRejected.Error()+": ")
	}
	if ferr := s.fail(ctx, sub, reason); ferr != nil {
		return errors.Join(err, ferr)
	}
	return err
}

func (s *Service) loadCases(ctx context.Context, ref problem.TestSetRef) ([]judge.TestCase, error) {
	return s.cases.get(ref.ID, func() ([]judge.TestCase, error) {
		r, err := s.problems.OpenTestSet(ctx, ref)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		cases, _, err := problem.ReadTestSet(r, true)
		return cases, err
	})
}

func ptrTime(t time.Time) *time.Time { return &t }
