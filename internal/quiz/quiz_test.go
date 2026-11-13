package quiz_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/quiz"
)

func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

type opt = struct {
	Body    string `json:"body"`
	Correct bool   `json:"correct"`
}

func question(kind quiz.Kind, prompt string, options ...opt) quiz.QuestionInput {
	explanation := "Because " + prompt
	return quiz.QuestionInput{Kind: kind, Prompt: prompt, Explanation: &explanation, Options: options}
}

// Two questions: a single-answer one (option 2 correct) and a multi-answer one (1 and 3).
var questions = []quiz.QuestionInput{
	question(quiz.KindSingle, "Binary search runs in?", opt{"O(n)", false}, opt{"O(log n)", true}, opt{"O(1)", false}),
	question(quiz.KindMulti, "Which are stable sorts?", opt{"Merge sort", true}, opt{"Heap sort", false}, opt{"Insertion sort", true}),
}

type fixture struct {
	e    *arenatest.Env
	svc  *quiz.Service
	ctx  context.Context
	quiz int64
}

// newFixture creates a quiz draft with an intro, by the arena author.
func newFixture(t *testing.T) *fixture {
	e := arenatest.New(t)
	f := &fixture{e: e, svc: quiz.NewService(e.DB, e.Contents, e.Reader), ctx: context.Background()}
	summary := "Check what you know about searching and sorting before moving on to graphs."
	d, err := f.svc.Create(f.ctx, e.Author, content.DraftInput{
		Meta: content.Meta{Title: "Searching and sorting", Slug: testenv.Unique("quiz"), Summary: &summary, CategoryID: &e.Category.ID},
		Body: "## Before you start\n\nTwo questions, pass at 70%.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.quiz = d.ID
	return f
}

func (f *fixture) publish(t *testing.T) {
	t.Helper()
	if _, err := f.svc.ReplaceQuestions(f.ctx, f.e.Author, f.quiz, questions); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Contents.Publish(f.ctx, f.e.Editor, f.quiz); err != nil {
		t.Fatal(err)
	}
}

func TestStudioValidatesQuestionsAndPublishNeedsSome(t *testing.T) {
	f := newFixture(t)
	bad := []struct {
		name string
		in   []quiz.QuestionInput
	}{
		{"no questions", nil},
		{"single with two correct", []quiz.QuestionInput{question(quiz.KindSingle, "Pick", opt{"a", true}, opt{"b", true})}},
		{"multi with none correct", []quiz.QuestionInput{question(quiz.KindMulti, "Pick", opt{"a", false}, opt{"b", false})}},
		{"one option", []quiz.QuestionInput{question(quiz.KindSingle, "Pick", opt{"a", true})}},
		{"unknown kind", []quiz.QuestionInput{question("ESSAY", "Pick", opt{"a", true}, opt{"b", false})}},
		{"directive in prompt", []quiz.QuestionInput{question(quiz.KindSingle, "::problem{slug=x}", opt{"a", true}, opt{"b", false})}},
	}
	for _, c := range bad {
		if _, err := f.svc.ReplaceQuestions(f.ctx, f.e.Author, f.quiz, c.in); code(err) != "validation_failed" {
			t.Errorf("%s: %v, want validation_failed", c.name, err)
		}
	}
	if _, err := f.svc.ReplaceQuestions(f.ctx, testenv.User(t, f.e.DB), f.quiz, questions); err == nil {
		t.Error("an outsider edited the questions")
	}

	if _, err := f.e.Contents.Publish(f.ctx, f.e.Editor, f.quiz); err == nil || !strings.Contains(errDetails(err), "quiz.no_questions") {
		t.Fatalf("publishing without questions: %v", err)
	}

	v, err := f.svc.ReplaceQuestions(f.ctx, f.e.Author, f.quiz, questions)
	if err != nil || len(v.Questions) != 2 || v.Version != 2 || len(v.Questions[1].Options) != 3 || v.Questions[1].Options[2].ID != 3 {
		t.Fatalf("replace = %+v, %v", v, err)
	}
	if _, err := f.svc.UpdateSettings(f.ctx, f.e.Author, f.quiz, quiz.Settings{PassPercent: 101}); code(err) != "validation_failed" {
		t.Errorf("pass mark 101: %v", err)
	}
}

func TestTakingAQuiz(t *testing.T) {
	f := newFixture(t)
	f.publish(t)
	reader := testenv.User(t, f.e.DB)

	page1, _, err := f.svc.PublicPage(f.ctx, "en", mustSlug(t, f))
	if err != nil || page1.Quiz.QuestionCount != 2 || page1.Quiz.PassPercent != 70 {
		t.Fatalf("public page = %+v, %v", page1, err)
	}

	sheet, err := f.svc.Questions(f.ctx, f.quiz)
	if err != nil || len(sheet.Questions) != 2 {
		t.Fatalf("sheet = %+v, %v", sheet, err)
	}
	raw, _ := json.Marshal(sheet)
	if strings.Contains(string(raw), "correct") || strings.Contains(string(raw), "Because") {
		t.Fatalf("the sheet leaks the answer key or explanations: %s", raw)
	}
	single, multi := sheet.Questions[0].ID, sheet.Questions[1].ID

	submit := func(answers ...quiz.Answer) quiz.Graded {
		t.Helper()
		g, err := f.svc.Submit(f.ctx, reader, f.quiz, sheet.Version, answers)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	// Multi needs exactly the correct set: one of the two is not enough.
	g := submit(quiz.Answer{QuestionID: single, OptionIDs: []int16{2}}, quiz.Answer{QuestionID: multi, OptionIDs: []int16{1}})
	if g.Correct != 1 || g.Percent != 50 || g.Passed || !g.Results[0].Correct || g.Results[1].Correct || g.Results[1].Explanation == nil {
		t.Fatalf("half right = %+v", g)
	}
	// Unanswered counts as wrong.
	if g = submit(quiz.Answer{QuestionID: multi, OptionIDs: []int16{3, 1}}); g.Correct != 1 || g.Results[0].Correct {
		t.Fatalf("unanswered single = %+v", g)
	}
	if g = submit(quiz.Answer{QuestionID: single, OptionIDs: []int16{2}}, quiz.Answer{QuestionID: multi, OptionIDs: []int16{1, 3}}); g.Percent != 100 || !g.Passed {
		t.Fatalf("all right = %+v", g)
	}

	// Malformed answers.
	if _, err := f.svc.Submit(f.ctx, reader, f.quiz, sheet.Version, []quiz.Answer{{QuestionID: single, OptionIDs: []int16{1, 2}}}); code(err) != "validation_failed" {
		t.Errorf("two picks on a single-answer question: %v", err)
	}
	if _, err := f.svc.Submit(f.ctx, reader, f.quiz, sheet.Version, []quiz.Answer{{QuestionID: single, OptionIDs: []int16{9}}}); code(err) != "validation_failed" {
		t.Errorf("unknown option: %v", err)
	}
	if _, err := f.svc.Submit(f.ctx, reader, f.quiz, sheet.Version, []quiz.Answer{{QuestionID: 1 << 40, OptionIDs: []int16{1}}}); code(err) != "quiz.changed" {
		t.Errorf("unknown question: %v", err)
	}

	states, err := f.svc.States(f.ctx, reader, []int64{f.quiz})
	if err != nil || len(states) != 1 || states[0].Attempts != 3 || states[0].BestPercent != 100 || states[0].LastPercent != 100 || states[0].PassedAt == nil {
		t.Fatalf("state = %+v, %v", states, err)
	}
	hist, err := f.svc.Attempts(f.ctx, reader, f.quiz, page.Request[quiz.AttemptCursor]{Limit: 2})
	if err != nil || len(hist.Items) != 2 || hist.NextCursor == nil || hist.Items[0].Percent != 100 {
		t.Fatalf("history = %+v, %v", hist, err)
	}

	// Editing the questions invalidates sheets handed out before.
	if _, err := f.svc.ReplaceQuestions(f.ctx, f.e.Editor, f.quiz, questions); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Submit(f.ctx, reader, f.quiz, sheet.Version, nil); code(err) != "quiz.changed" {
		t.Errorf("stale sheet: %v, want quiz.changed", err)
	}

	if err := f.e.DB.InTx(f.ctx, func(tx pgx.Tx) error { return quiz.EraseUser(f.ctx, tx, reader.UserID) }); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.svc.States(f.ctx, reader, []int64{f.quiz}); len(s) != 0 {
		t.Errorf("state survived erase: %+v", s)
	}
}

// An article can embed a quiz only once the quiz is live; the embedded quiz then becomes one
// of the article's practice links, which is how the page renders its title.
func TestEmbeddedQuizMustBeLive(t *testing.T) {
	f := newFixture(t)
	summary := "Practise binary search with a short quiz right after the explanation on this page."
	d, err := f.e.Contents.Create(f.ctx, f.e.Author, content.CreateInput{Type: content.TypeArticle, DraftInput: content.DraftInput{
		Meta: content.Meta{Title: "Binary search", Slug: testenv.Unique("bs"), Summary: &summary, CategoryID: &f.e.Category.ID},
		Body: "## Try it\n\n::quiz{id=" + itoa(f.quiz) + "}\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.Contents.Publish(f.ctx, f.e.Editor, d.ID); err == nil || !strings.Contains(errDetails(err), "quiz.missing") {
		t.Fatalf("embedding an unpublished quiz: %v", err)
	}
	f.publish(t)
	if _, err := f.e.Contents.Publish(f.ctx, f.e.Editor, d.ID); err != nil {
		t.Fatal(err)
	}
	links, err := f.e.DB.Primary.Query(f.ctx, "SELECT to_id FROM content_link WHERE from_id = $1 AND kind = 'PRACTICE'", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(links, pgx.RowTo[int64])
	if err != nil || len(ids) != 1 || ids[0] != f.quiz {
		t.Fatalf("practice links = %v, %v; want the quiz", ids, err)
	}
}

func mustSlug(t *testing.T, f *fixture) string {
	t.Helper()
	var slug string
	if err := f.e.DB.Primary.QueryRow(f.ctx, "SELECT slug FROM content WHERE id = $1", f.quiz).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	return slug
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// errDetails renders an error with its details, where publish issues are listed.
func errDetails(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		b, _ := json.Marshal(ae.Details)
		return ae.Code + " " + string(b)
	}
	return err.Error()
}
