package problem_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/taxonomy"
)

func cases(tag string, n int) []judge.TestCase {
	out := make([]judge.TestCase, n)
	for i := range out {
		out[i] = judge.TestCase{Stdin: fmt.Sprintf("%s %d", tag, i), Expected: fmt.Sprint(i)}
	}
	return out
}

func wantCode(t *testing.T, err error, code string) *apperr.Error {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return e
}

func TestProblemCannotPublishUntilJudgeable(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	d := e.Draft(testenv.Unique("p"))

	_, err := e.Contents.Publish(ctx, e.Editor, d.ID)
	issues := fmt.Sprint(wantCode(t, err, "content.not_publishable").Details)
	for _, c := range []string{"problem.no_tests", "problem.no_samples", "problem.no_templates"} {
		if !strings.Contains(issues, c) {
			t.Fatalf("missing %s in %s", c, issues)
		}
	}

	// Template drivers must contain the placeholder exactly once.
	bad := "print(1)"
	_, err = e.Problems.PutTemplate(ctx, e.Author, d.ID, problem.Template{Language: "python", StarterCode: "x", DriverCode: &bad})
	wantCode(t, err, "validation_failed")
	_, err = e.Problems.PutTemplate(ctx, e.Author, d.ID, problem.Template{Language: "cobol", StarterCode: "x"})
	wantCode(t, err, "validation_failed")
}

func TestMalformedAndForeignTestSetsRejected(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	d := e.Draft(testenv.Unique("p"))
	other := testenv.User(t, e.DB)
	foreign := e.UploadTestSet(other, cases(testenv.Unique("f"), 2))
	_, err := e.Problems.AddTestSet(ctx, e.Author, d.ID, foreign)
	wantCode(t, err, "validation_failed")
}

func TestPublishedProblemLifecycle(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tag := testenv.Unique("sum")
	driver := "import sys\n{{USER_CODE}}\nprint(solve(*map(int, sys.stdin.read().split())))\n"
	id := e.Publish(tag, []problem.Sample{{Input: "1 2", Output: "3"}}, cases(tag, 150), &driver)

	v, err := e.Problems.Studio(ctx, e.Editor, id)
	if err != nil || len(v.TestSets) != 1 || !v.TestSets[0].Active || v.TestSets[0].CaseCount != 150 {
		t.Fatalf("studio = %+v, %v", v.TestSets, err)
	}
	// Once live, judging data is editor-only: the author's edits would change a live problem.
	_, err = e.Problems.ReplaceSamples(ctx, e.Author, id, nil)
	wantCode(t, err, "content.not_editable")

	p, redirect, err := e.Problems.Page(ctx, "en", tag)
	if err != nil || redirect != nil {
		t.Fatalf("page: %v %v", redirect, err)
	}
	if p.Problem.Difficulty != problem.Easy || len(p.Problem.Samples) != 1 || len(p.Problem.Templates) != 1 || p.Problem.AcceptanceRate != nil {
		t.Fatalf("detail = %+v", p.Problem)
	}

	spec, err := e.Problems.Judging(ctx, id)
	if err != nil || spec.TestSet == nil || spec.Drivers["python"] != driver || spec.TimeLimitMs != 1000 {
		t.Fatalf("judging spec = %+v, %v", spec, err)
	}

	easy := problem.Easy
	listed := func() bool {
		res, err := e.Problems.List(ctx, problem.ListFilter{Difficulty: &easy}, page.Request[content.PublishedCursor]{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range res.Items {
			if it.ID == id {
				return true
			}
		}
		return false
	}
	if !listed() {
		t.Fatal("published problem missing from the EASY listing")
	}
	if _, err := e.Contents.Archive(ctx, e.Editor, id); err != nil {
		t.Fatal(err)
	}
	if listed() {
		t.Fatal("archived problem still listed")
	}

	// Problem of the day needs a site-wide editor and a live problem.
	if _, err := e.Contents.Publish(ctx, e.Editor, id); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC()
	wantCode(t, e.Problems.ScheduleOfDay(ctx, e.Editor, day, id), "access.denied")
	global := testenv.User(t, e.DB)
	testenv.Grant(t, e.DB, global, "EDITOR", nil)
	if err := e.Problems.ScheduleOfDay(ctx, global, day, id); err != nil {
		t.Fatal(err)
	}
	today, err := e.Problems.Today(ctx)
	if err != nil || today.ID != id {
		t.Fatalf("today = %+v, %v", today.ID, err)
	}
}

// The statement, examples, starter code and hints are part of the cached problem page, so
// editing any of them on a live problem refreshes it like a publish does.
func TestJudgingEditsRefreshTheLivePage(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tag := testenv.Unique("touch")
	id := e.Publish(tag, []problem.Sample{{Input: "1 2", Output: "3"}}, cases(tag, 3), nil)
	updates := func() (n int) {
		if err := e.DB.Primary.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic = 'content.updated' AND aggregate_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	edits := map[string]func() error{
		"samples": func() error {
			_, err := e.Problems.ReplaceSamples(ctx, e.Editor, id, []problem.Sample{{Input: "2 2", Output: "4"}})
			return err
		},
		"template": func() error {
			_, err := e.Problems.PutTemplate(ctx, e.Editor, id, problem.Template{Language: "go", StarterCode: "package main"})
			return err
		},
		"template removed": func() error {
			_, err := e.Problems.DeleteTemplate(ctx, e.Editor, id, "go")
			return err
		},
		"hints": func() error {
			_, err := e.Problems.ReplaceHints(ctx, e.Editor, id, []string{"Try two pointers."})
			return err
		},
	}
	for name, edit := range edits {
		before := updates()
		if err := edit(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if updates() != before+1 {
			t.Errorf("%s: the live page was not refreshed", name)
		}
	}
}

// A random pick is always a listed problem, and filters that match nothing say so.
func TestRandomPicksListedProblems(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	// Packages share the test database, so a pick may be another test's problem; what matters
	// is that it is always a listed problem.
	for range 3 {
		tag := testenv.Unique("rnd")
		e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)
	}
	for range 20 {
		item, err := e.Problems.Random(ctx, problem.RandomFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if item.Type != "PROBLEM" || item.Difficulty == "" {
			t.Fatalf("random pick = %s with difficulty %q", item.Type, item.Difficulty)
		}
	}
	if _, err := e.Problems.Random(ctx, problem.RandomFilter{Tag: "company:" + testenv.Unique("nobody")}); err == nil {
		t.Fatal("unknown tag must not pick anything")
	}
	bad := problem.Difficulty("IMPOSSIBLE")
	if _, err := e.Problems.Random(ctx, problem.RandomFilter{Difficulty: &bad}); err == nil {
		t.Fatal("invalid difficulty accepted")
	}
}

// A pick within a tag walks the tag through the content domain and keeps listed problems of
// the requested difficulty.
func TestRandomWithinTag(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tax := taxonomy.NewService(e.DB, access.NewPolicy(e.DB.Primary))
	tag, err := tax.CreateTag(ctx, testenv.Admin(t, e.DB), taxonomy.CreateTag{Kind: taxonomy.TagCompany, Slug: testenv.Unique("co"), Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	slug := testenv.Unique("tagged")
	id := e.Publish(slug, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: slug, Expected: "1"}}, nil, tag.ID)
	filter := "company:" + tag.Slug

	for range 5 {
		item, err := e.Problems.Random(ctx, problem.RandomFilter{Tag: filter})
		if err != nil || item.ID != id {
			t.Fatalf("tagged pick = %d, %v; want %d", item.ID, err, id)
		}
	}
	easy, hard := problem.Easy, problem.Hard
	if item, err := e.Problems.Random(ctx, problem.RandomFilter{Tag: filter, Difficulty: &easy}); err != nil || item.ID != id {
		t.Fatalf("tagged easy pick = %d, %v", item.ID, err)
	}
	if _, err := e.Problems.Random(ctx, problem.RandomFilter{Tag: filter, Difficulty: &hard}); err == nil {
		t.Fatal("hard pick from a tag with only an easy problem")
	}
}
