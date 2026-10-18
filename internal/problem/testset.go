package problem

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"code-sprout-cms/internal/judge"
)

// A test set file is NDJSON: one {"input": "...", "output": "..."} object per line, in the
// order cases run. Hidden cases never leave the server.

const (
	MaxTestCases   = 100_000
	maxCaseBytes   = 16 << 20 // per input or output
	maxSetMemBytes = 512 << 20
)

type testCaseLine struct {
	Input  *string `json:"input"`
	Output *string `json:"output"`
}

// ErrBadTestSet wraps every validation failure, with the 1-based case number.
var ErrBadTestSet = errors.New("invalid test set")

// ReadTestSet streams and validates a test set. With collect=false only counts are kept, so
// validating a large upload costs constant memory; the runner collects the cases.
func ReadTestSet(r io.Reader, collect bool) (cases []judge.TestCase, count int, err error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var held int
	for {
		var line testCaseLine
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, count, fmt.Errorf("%w: case %d: %v", ErrBadTestSet, count+1, err)
		}
		count++
		if count > MaxTestCases {
			return nil, count, fmt.Errorf("%w: more than %d cases", ErrBadTestSet, MaxTestCases)
		}
		if line.Input == nil || line.Output == nil {
			return nil, count, fmt.Errorf("%w: case %d: needs both \"input\" and \"output\"", ErrBadTestSet, count)
		}
		if len(*line.Input) > maxCaseBytes || len(*line.Output) > maxCaseBytes {
			return nil, count, fmt.Errorf("%w: case %d: input or output exceeds 16 MiB", ErrBadTestSet, count)
		}
		// The judge receives each case JSON-encoded inside a size-capped request.
		if judge.CaseSize(judge.TestCase{Stdin: *line.Input, Expected: *line.Output}) > judge.MaxCaseBytes {
			return nil, count, fmt.Errorf("%w: case %d: input and output together are too large for the judge (%d MiB encoded at most)",
				ErrBadTestSet, count, judge.MaxCaseBytes>>20)
		}
		if collect {
			held += len(*line.Input) + len(*line.Output)
			if held > maxSetMemBytes {
				return nil, count, fmt.Errorf("%w: cases exceed %d MiB in total", ErrBadTestSet, maxSetMemBytes>>20)
			}
			cases = append(cases, judge.TestCase{Stdin: *line.Input, Expected: *line.Output})
		}
	}
	if count == 0 {
		return nil, 0, fmt.Errorf("%w: no cases", ErrBadTestSet)
	}
	return cases, count, nil
}
