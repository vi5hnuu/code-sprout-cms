// Package judge is the client for ls-judge (WebstormProjects/ls-judge), which runs untrusted
// code in Firecracker microVMs. This service never executes code itself.
//
// Integration model: the CMS calls the judge service-to-service with one API key. Jobs are
// owned by that key on the judge, so browsers never talk to the judge directly; the CMS
// records which reader started each run and relays results (see internal/submission).
//
// Judge limits that shape callers: at most MaxCasesPerJob test cases per job and
// MaxJobsPerBatch jobs per /batch/execute call.
package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	MaxCasesPerJob  = 100
	MaxJobsPerBatch = 20
	MaxCodeBytes    = 64 << 10
	MaxTimeLimitMs  = 30_000
	MaxMemoryMB     = 1024
)

type Verdict string

const (
	VerdictAC  Verdict = "AC"
	VerdictWA  Verdict = "WA"
	VerdictTLE Verdict = "TLE"
	VerdictMLE Verdict = "MLE"
	VerdictRE  Verdict = "RE"
	VerdictCE  Verdict = "CE"
	VerdictIE  Verdict = "IE" // judge-side failure: not the user's fault, safe to retry
)

type TestCase struct {
	Stdin    string `json:"stdin"`
	Expected string `json:"expected"`
}

// ExecuteRequest. Language is a base language ("python") or an exact runtime id
// ("python-3.13"); the judge resolves it. (Its OpenAPI calls the field runtime_id, but the
// handler decodes "language".)
type ExecuteRequest struct {
	Language    string     `json:"language"`
	Code        string     `json:"code"`
	TimeLimitMs int        `json:"time_limit_ms"`
	MemLimitMB  int        `json:"mem_limit_mb"`
	TestCases   []TestCase `json:"test_cases"`
}

type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

func (s JobStatus) Finished() bool { return s == JobDone || s == JobFailed }

type CaseResult struct {
	Index   int     `json:"index"`
	Stdout  *string `json:"stdout,omitempty"`
	Stderr  *string `json:"stderr,omitempty"`
	Verdict Verdict `json:"verdict"`
	TimeMs  *int    `json:"time_ms,omitempty"`
}

type JobResult struct {
	JobID       string       `json:"job_id"`
	Status      JobStatus    `json:"status"`
	Verdict     *Verdict     `json:"verdict,omitempty"`
	TotalTimeMs *int         `json:"total_time_ms,omitempty"`
	Results     []CaseResult `json:"results"`
}

var (
	// ErrRejected: the judge refused the request itself (bad language, limits). Not retryable.
	ErrRejected = errors.New("judge rejected the request")
	// ErrBusy: quota or rate limit on the judge side (402/429). Retry later.
	ErrBusy = errors.New("judge is busy")
	// ErrUnavailable: no judge configured, or it cannot be reached. Retry later.
	ErrUnavailable = errors.New("code execution is unavailable")
	ErrNotFound    = errors.New("judge job not found")
)

type Client struct {
	base     string
	apiKey   string
	runtimes map[string]string
	http     *http.Client
}

// New builds a client; an empty baseURL yields a client whose calls fail with ErrUnavailable
// (local development without a judge). runtimes maps each language to the exact judge runtime
// id it runs on; callers keep using plain language names.
func New(baseURL, apiKey string, runtimes map[string]string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), apiKey: apiKey, runtimes: runtimes, http: &http.Client{Timeout: 15 * time.Second}}
}

// pinned returns req with its language replaced by the pinned runtime id, when there is one.
func (c *Client) pinned(req ExecuteRequest) ExecuteRequest {
	if id, ok := c.runtimes[req.Language]; ok {
		req.Language = id
	}
	return req
}

// Execute submits one job. idempotencyKey makes retries safe: the judge replays the original
// job for a repeated key instead of creating a second one.
func (c *Client) Execute(ctx context.Context, req ExecuteRequest, idempotencyKey string) (string, error) {
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/execute", c.pinned(req), idempotencyKey, &out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

// ExecuteBatch submits up to MaxJobsPerBatch jobs. If the judge refuses any of them the whole
// batch is reported as rejected (a partially judged submission would be meaningless).
func (c *Client) ExecuteBatch(ctx context.Context, reqs []ExecuteRequest, idempotencyKey string) ([]string, error) {
	if len(reqs) > MaxJobsPerBatch {
		return nil, fmt.Errorf("%w: batch of %d exceeds %d", ErrRejected, len(reqs), MaxJobsPerBatch)
	}
	var out struct {
		Accepted []struct {
			Index int    `json:"index"`
			JobID string `json:"job_id"`
		} `json:"accepted"`
		Errors []struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	pinned := make([]ExecuteRequest, len(reqs))
	for i, r := range reqs {
		pinned[i] = c.pinned(r)
	}
	// ls-judge takes the jobs as a bare JSON array and answers {accepted, errors} by index.
	if err := c.do(ctx, http.MethodPost, "/batch/execute", pinned, idempotencyKey, &out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("%w: job %d: %s", ErrRejected, out.Errors[0].Index, out.Errors[0].Error)
	}
	ids := make([]string, len(reqs))
	for _, a := range out.Accepted {
		if a.Index >= 0 && a.Index < len(ids) {
			ids[a.Index] = a.JobID
		}
	}
	for i, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("judge accepted no job for batch index %d", i)
		}
	}
	return ids, nil
}

func (c *Client) Get(ctx context.Context, jobID string) (JobResult, error) {
	var out JobResult
	err := c.do(ctx, http.MethodGet, "/jobs/"+url.PathEscape(jobID), nil, "", &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	if c.base == "" {
		return ErrUnavailable
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrRejected, judgeMessage(resp.Body))
	case resp.StatusCode == http.StatusPaymentRequired || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrBusy, judgeMessage(resp.Body))
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		// A bad or rotated API key is an operator problem: keep work queued (retryable) and
		// make it loud, rather than failing every reader's submission.
		slog.Error("judge rejected the CMS API key; check JUDGE_API_KEY", "status", resp.StatusCode)
		return fmt.Errorf("%w: judge authentication failed", ErrUnavailable)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("judge %s %s: unexpected status %d: %s", method, path, resp.StatusCode, judgeMessage(resp.Body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// judgeMessage extracts the text from the judge's {"error": {"code", "message"}} envelope.
func judgeMessage(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Code + ": " + e.Error.Message
	}
	return strings.TrimSpace(string(raw))
}
