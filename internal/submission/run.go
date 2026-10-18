package submission

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/apperr"
)

// Runs are not stored: the judge keeps the job, and Redis remembers who started it (for the
// TTL below) so only that reader can fetch the result. Jobs belong to the CMS's judge account,
// which is why results are relayed through here instead of read from the judge directly.

const (
	runTTL          = 15 * time.Minute
	snippetTimeMs   = 5000
	snippetMemoryMB = 256
	maxStdinBytes   = 64 << 10
	runKeyPrefix    = "run:"
)

type runKind string

const (
	// Free-form runs (snippets, custom input) have no expected output, so the judge's
	// "wrong answer" just means "ran"; such results are reported as OK.
	runFree    runKind = "free"
	runSamples runKind = "samples"
)

type RunTicket struct {
	RunID string `json:"runId"`
	// PollAfterMs suggests how long to wait before the first GET of the result.
	PollAfterMs int `json:"pollAfterMs"`
}

type SnippetInput struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Stdin    string `json:"stdin"`
}

type ProblemRunInput struct {
	Language string  `json:"language"`
	Code     string  `json:"code"`
	Input    *string `json:"input"` // custom input; omitted = run the samples
}

// RunSnippet runs a code block from an article (or the playground). owner identifies the
// reader: "u<id>" when signed in, else a client address.
func (s *Service) RunSnippet(ctx context.Context, owner string, in SnippetInput) (RunTicket, error) {
	if err := validateCode(in.Language, in.Code); err != nil {
		return RunTicket{}, err
	}
	if len(in.Stdin) > maxStdinBytes {
		return RunTicket{}, apperr.ValidationFailed(map[string]string{"stdin": "Input is at most 64 KiB."})
	}
	return s.startRun(ctx, owner, runFree, judge.ExecuteRequest{
		Language: in.Language, Code: in.Code, TimeLimitMs: snippetTimeMs, MemLimitMB: snippetMemoryMB,
		TestCases: []judge.TestCase{{Stdin: in.Stdin}},
	})
}

// RunProblem runs code against a problem's samples (or custom input) with the problem's
// limits and driver. Nothing is recorded: only submissions count.
func (s *Service) RunProblem(ctx context.Context, owner string, problemID int64, in ProblemRunInput) (RunTicket, error) {
	if err := validateCode(in.Language, in.Code); err != nil {
		return RunTicket{}, err
	}
	spec, err := s.requireLiveProblem(ctx, problemID)
	if err != nil {
		return RunTicket{}, err
	}
	req := judge.ExecuteRequest{
		Language: in.Language, Code: SourceFor(spec, in.Language, in.Code), TimeLimitMs: spec.TimeLimitMs, MemLimitMB: spec.MemoryMB,
	}
	kind := runSamples
	if in.Input != nil {
		if len(*in.Input) > maxStdinBytes {
			return RunTicket{}, apperr.ValidationFailed(map[string]string{"input": "Input is at most 64 KiB."})
		}
		kind = runFree
		req.TestCases = []judge.TestCase{{Stdin: *in.Input}}
	} else {
		for _, sm := range spec.Samples {
			req.TestCases = append(req.TestCases, judge.TestCase{Stdin: sm.Input, Expected: sm.Output})
		}
		if len(req.TestCases) == 0 {
			return RunTicket{}, apperr.Conflict("problem.no_samples", "This problem has no samples to run.")
		}
	}
	return s.startRun(ctx, owner, kind, req)
}

func (s *Service) startRun(ctx context.Context, owner string, kind runKind, req judge.ExecuteRequest) (RunTicket, error) {
	jobID, err := s.judge.Execute(ctx, req, "")
	if err != nil {
		return RunTicket{}, judgeError(err)
	}
	if err := s.rdb.Set(ctx, runKeyPrefix+jobID, owner+"|"+string(kind), runTTL).Err(); err != nil {
		return RunTicket{}, err
	}
	return RunTicket{RunID: jobID, PollAfterMs: 400}, nil
}

type RunCase struct {
	Index   int     `json:"index"`
	Verdict string  `json:"verdict"`
	Stdout  *string `json:"stdout"`
	Stderr  *string `json:"stderr"`
	TimeMs  *int    `json:"timeMs"`
}

type RunResult struct {
	Status string    `json:"status"` // pending | running | done | failed
	Done   bool      `json:"done"`
	Cases  []RunCase `json:"cases"`
	// Message explains a run that failed without results (the judge reports no reason itself).
	Message *string `json:"message,omitempty"`
}

const runFailedMessage = "The judge could not run this code. Please try again in a moment."

// RunResult relays a run's progress to the reader who started it.
func (s *Service) RunResult(ctx context.Context, owner, runID string) (RunResult, error) {
	notFound := apperr.NotFound("run.not_found", "No such run, or it has expired.")
	val, err := s.rdb.Get(ctx, runKeyPrefix+runID).Result()
	if errors.Is(err, redis.Nil) {
		return RunResult{}, notFound
	}
	if err != nil {
		return RunResult{}, err
	}
	who, kind, _ := strings.Cut(val, "|")
	if who != owner {
		return RunResult{}, notFound
	}
	job, err := s.judge.Get(ctx, runID)
	if errors.Is(err, judge.ErrNotFound) {
		return RunResult{}, notFound
	}
	if err != nil {
		return RunResult{}, judgeError(err)
	}
	out := RunResult{Status: string(job.Status), Done: job.Status.Finished(), Cases: make([]RunCase, 0, len(job.Results))}
	if c := job.Compile; c != nil {
		// A compile error is the run's only outcome: show it as the first case.
		out.Cases = append(out.Cases, RunCase{Index: 0, Verdict: string(judge.VerdictCE), Stdout: c.Stdout, Stderr: c.Stderr, TimeMs: c.TimeMs})
		return out, nil
	}
	if job.Status == judge.JobFailed && len(job.Results) == 0 {
		msg := runFailedMessage
		out.Message = &msg
		return out, nil
	}
	for _, c := range job.Results {
		v := string(c.Verdict)
		if runKind(kind) == runFree && (c.Verdict == judge.VerdictAC || c.Verdict == judge.VerdictWA) {
			v = "OK"
		}
		out.Cases = append(out.Cases, RunCase{Index: c.Index, Verdict: v, Stdout: c.Stdout, Stderr: c.Stderr, TimeMs: c.TimeMs})
	}
	return out, nil
}

// judgeError turns judge failures into errors readers can act on.
func judgeError(err error) error {
	switch {
	case errors.Is(err, judge.ErrRejected):
		return apperr.Invalid("run.rejected", strings.TrimPrefix(err.Error(), judge.ErrRejected.Error()+": "))
	case errors.Is(err, judge.ErrBusy):
		return apperr.RateLimited("The code runner is busy. Try again in a moment.")
	case errors.Is(err, judge.ErrUnavailable):
		return &apperr.Error{Kind: apperr.KindUnavailable, Code: "run.unavailable", Detail: "Running code is temporarily unavailable.", Err: err}
	}
	return err
}
