package judge

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// ls-judge rejects request bodies over MAX_BODY_BYTES (default 32 MiB) for one job and
// MAX_BATCH_BODY_BYTES (default 64 MiB) for a batch. Plans stay below both with headroom for
// the request envelope, so a large test set is split by size as well as by case count.
const (
	MaxJobBytes   = 30 << 20
	MaxBatchBytes = 60 << 20
	// MaxCaseBytes bounds one case's encoded input plus expected output, so that any single
	// case always fits in a job beside the code: a driver and the submission are each at most
	// MaxCodeBytes, at most 6x that once JSON-escaped, well inside the 1 MiB left over.
	MaxCaseBytes = MaxJobBytes - 1<<20

	jobOverhead  = 256 // the request's fixed JSON fields
	caseOverhead = 32  // {"stdin":"","expected":""},
)

// ErrCaseTooLarge means a single test case cannot fit in any judge request.
var ErrCaseTooLarge = errors.New("test case too large for the judge")

// Span is a run of consecutive cases, [Start, End), sent as one judge job.
type Span struct{ Start, End int }

// EncodedLen is the length of s as a JSON string body (without quotes), as encoding/json
// writes it: control characters, quotes, backslashes and <, >, & are escaped.
func EncodedLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
			n += 2
			i++
		case c < 0x20 || c == '<' || c == '>' || c == '&':
			n += 6
			i++
		case c < utf8.RuneSelf:
			n++
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			switch {
			case r == utf8.RuneError && size == 1:
				n += 3 // an invalid byte is written as U+FFFD (3 bytes of UTF-8)
			case r == '\u2028' || r == '\u2029':
				n += 6 // line and paragraph separators are escaped
			default:
				n += size
			}
			i += size
		}
	}
	return n
}

// CaseSize is the encoded size of one case inside a job request.
func CaseSize(c TestCase) int { return EncodedLen(c.Stdin) + EncodedLen(c.Expected) + caseOverhead }

// PlanJobs splits cases into consecutive jobs of at most MaxCasesPerJob cases whose requests
// (with the code) stay under MaxJobBytes. It is deterministic in its inputs, so the runner can
// recompute the same spans when it resumes a submission whose jobs were already sent.
func PlanJobs(cases []TestCase, code string) ([]Span, error) {
	base := jobOverhead + EncodedLen(code)
	var spans []Span
	start, size := 0, base
	for i, c := range cases {
		cs := CaseSize(c)
		if cs > MaxCaseBytes || base+cs > MaxJobBytes {
			return nil, fmt.Errorf("%w: case %d is %d bytes encoded", ErrCaseTooLarge, i+1, cs)
		}
		if i > start && (i-start == MaxCasesPerJob || size+cs > MaxJobBytes) {
			spans = append(spans, Span{start, i})
			start, size = i, base
		}
		size += cs
	}
	if start < len(cases) {
		spans = append(spans, Span{start, len(cases)})
	}
	return spans, nil
}

// PlanBatches groups consecutive jobs into /batch/execute calls of at most MaxJobsPerBatch
// jobs and MaxBatchBytes in total. sizes are the jobs' encoded request sizes.
func PlanBatches(sizes []int) []Span {
	var batches []Span
	start, total := 0, 0
	for i, s := range sizes {
		if i > start && (i-start == MaxJobsPerBatch || total+s > MaxBatchBytes) {
			batches = append(batches, Span{start, i})
			start, total = i, 0
		}
		total += s
	}
	if start < len(sizes) {
		batches = append(batches, Span{start, len(sizes)})
	}
	return batches
}

// JobSize is the encoded request size of the job for cases[span].
func JobSize(cases []TestCase, code string, span Span) int {
	n := jobOverhead + EncodedLen(code)
	for _, c := range cases[span.Start:span.End] {
		n += CaseSize(c)
	}
	return n
}
