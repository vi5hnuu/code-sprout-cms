// Package quiz builds the QUIZ content type: an intro (the content body, edited and reviewed
// like any article) plus multiple-choice questions, taken by signed-in readers and graded on
// the server. It follows the problem package's shape: content owns the item's lifecycle and
// this package owns the type's own tables, joined through content.RegisterType hooks.
package quiz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/quiz/quizdb"
)

// Kind is how a question is answered.
type Kind string

const (
	KindSingle Kind = "SINGLE" // exactly one option is correct; the reader picks one
	KindMulti  Kind = "MULTI"  // one or more are correct; the reader must pick exactly those
)

const (
	maxQuestions    = 100
	minOptions      = 2
	maxOptions      = 10
	maxPromptLen    = 4000
	maxExplainLen   = 4000
	maxOptionLen    = 1000
	defaultPassMark = 70
)

// Option is one choice. Correct is only ever sent to Studio, never to readers.
type Option struct {
	ID      int16  `json:"id"`
	Body    string `json:"body"`
	Correct bool   `json:"correct"`
}

// Question as authored (Studio view).
type Question struct {
	ID          int64    `json:"id"`
	Kind        Kind     `json:"kind"`
	Prompt      string   `json:"prompt"`
	Explanation *string  `json:"explanation"`
	Options     []Option `json:"options"`
}

// Settings are the quiz-wide rules.
type Settings struct {
	PassPercent int  `json:"passPercent"`
	Shuffle     bool `json:"shuffle"`
}

// StudioView is everything Studio edits on a quiz.
type StudioView struct {
	ContentID int64      `json:"contentId"`
	Version   int32      `json:"version"`
	Settings  Settings   `json:"settings"`
	Questions []Question `json:"questions"`
}

// QuestionInput is one question as Studio sends it; options get ids 1..n in order.
type QuestionInput struct {
	Kind        Kind    `json:"kind"`
	Prompt      string  `json:"prompt"`
	Explanation *string `json:"explanation"`
	Options     []struct {
		Body    string `json:"body"`
		Correct bool   `json:"correct"`
	} `json:"options"`
}

// Reader is what quizzes need from the content read side (implemented by content.Reader).
type Reader interface {
	Page(ctx context.Context, t content.Type, locale, slug string) (*content.PublicPage, *content.Redirect, error)
	LiveIDs(ctx context.Context, ids []int64) ([]int64, error)
}

type Service struct {
	db       *db.DB
	contents *content.Service
	reader   Reader
}

// NewService also registers the QUIZ type's publish check with the content service.
func NewService(d *db.DB, contents *content.Service, reader Reader) *Service {
	s := &Service{db: d, contents: contents, reader: reader}
	contents.RegisterType(content.TypeQuiz, content.TypeHooks{Check: s.publishIssues})
	return s
}

var errNotFound = apperr.NotFound("quiz.not_found", "No such quiz.")

// Create starts a quiz: a QUIZ content item (its intro) plus its settings row.
func (s *Service) Create(ctx context.Context, p auth.Principal, in content.DraftInput) (content.Draft, error) {
	return s.contents.CreateTyped(ctx, p, content.CreateInput{Type: content.TypeQuiz, DraftInput: in},
		func(ctx context.Context, tx pgx.Tx, id int64) error { return quizdb.New(tx).InsertQuizExt(ctx, id) })
}

// Studio returns the quiz as its editors see it, answer key included.
func (s *Service) Studio(ctx context.Context, p auth.Principal, id int64) (StudioView, error) {
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeQuiz); err != nil {
		return StudioView{}, err
	}
	return s.studioView(ctx, quizdb.New(s.db.Primary), id)
}

func (s *Service) studioView(ctx context.Context, q *quizdb.Queries, id int64) (StudioView, error) {
	ext, err := q.GetQuizExt(ctx, id)
	if db.IsNoRows(err) {
		return StudioView{}, errNotFound
	}
	if err != nil {
		return StudioView{}, err
	}
	qs, err := loadQuestions(ctx, q, id)
	if err != nil {
		return StudioView{}, err
	}
	return StudioView{ContentID: id, Version: ext.Version, Settings: Settings{PassPercent: int(ext.PassPercent), Shuffle: ext.Shuffle}, Questions: qs}, nil
}

func (s *Service) UpdateSettings(ctx context.Context, p auth.Principal, id int64, in Settings) (StudioView, error) {
	if in.PassPercent == 0 {
		in.PassPercent = defaultPassMark
	}
	if in.PassPercent < 1 || in.PassPercent > 100 {
		return StudioView{}, apperr.ValidationFailed(map[string]string{"passPercent": "The pass mark must be 1-100%."})
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeQuiz); err != nil {
		return StudioView{}, err
	}
	q := quizdb.New(s.db.Primary)
	if err := q.UpdateQuizSettings(ctx, quizdb.UpdateQuizSettingsParams{PassPercent: int16(in.PassPercent), Shuffle: in.Shuffle, ContentID: id}); err != nil {
		return StudioView{}, err
	}
	return s.studioView(ctx, q, id)
}

// ReplaceQuestions swaps the whole question set (Studio edits the list as a unit) and bumps
// the version, so a reader who started on the old set is told it changed instead of being
// graded against questions they never saw.
func (s *Service) ReplaceQuestions(ctx context.Context, p auth.Principal, id int64, in []QuestionInput) (StudioView, error) {
	rows, err := validateQuestions(in)
	if err != nil {
		return StudioView{}, err
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeQuiz); err != nil {
		return StudioView{}, err
	}
	var view StudioView
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := quizdb.New(tx)
		if _, err := q.BumpQuizVersion(ctx, id); err != nil {
			if db.IsNoRows(err) {
				return errNotFound
			}
			return err
		}
		if err := q.DeleteQuestions(ctx, id); err != nil {
			return err
		}
		for i, r := range rows {
			r.QuizID, r.Position = id, int16(i)
			if err := q.InsertQuestion(ctx, r); err != nil {
				return err
			}
		}
		view, err = s.studioView(ctx, q, id)
		return err
	})
	return view, err
}

// validateQuestions checks the set and turns it into rows. Text is Sprout comment markdown
// (inline formatting and code, no directives or HTML), validated like a comment.
func validateQuestions(in []QuestionInput) ([]quizdb.InsertQuestionParams, error) {
	if len(in) == 0 || len(in) > maxQuestions {
		return nil, apperr.ValidationFailed(map[string]string{"questions": fmt.Sprintf("A quiz has 1-%d questions.", maxQuestions)})
	}
	f := map[string]string{}
	rows := make([]quizdb.InsertQuestionParams, 0, len(in))
	for i, qi := range in {
		field := func(name string) string { return fmt.Sprintf("questions[%d].%s", i, name) }
		if qi.Kind != KindSingle && qi.Kind != KindMulti {
			f[field("kind")] = "kind must be SINGLE or MULTI."
		}
		prompt := strings.TrimSpace(qi.Prompt)
		checkText(f, field("prompt"), prompt, maxPromptLen, true)
		var explanation *string
		if qi.Explanation != nil {
			if e := strings.TrimSpace(*qi.Explanation); e != "" {
				checkText(f, field("explanation"), e, maxExplainLen, false)
				explanation = &e
			}
		}
		if len(qi.Options) < minOptions || len(qi.Options) > maxOptions {
			f[field("options")] = fmt.Sprintf("Give %d-%d options.", minOptions, maxOptions)
		}
		opts := make([]Option, len(qi.Options))
		correct := 0
		for j, o := range qi.Options {
			body := strings.TrimSpace(o.Body)
			checkText(f, fmt.Sprintf("questions[%d].options[%d]", i, j), body, maxOptionLen, true)
			opts[j] = Option{ID: int16(j + 1), Body: body, Correct: o.Correct}
			if o.Correct {
				correct++
			}
		}
		switch {
		case qi.Kind == KindSingle && correct != 1:
			f[field("options")] = "Mark exactly one option correct."
		case qi.Kind == KindMulti && correct == 0:
			f[field("options")] = "Mark at least one option correct."
		}
		raw, _ := json.Marshal(opts)
		rows = append(rows, quizdb.InsertQuestionParams{Kind: string(qi.Kind), PromptMd: prompt, ExplanationMd: explanation, Options: raw})
	}
	if len(f) > 0 {
		return nil, apperr.ValidationFailed(f)
	}
	return rows, nil
}

func checkText(f map[string]string, field, text string, maxLen int, required bool) {
	switch {
	case text == "" && required:
		f[field] = "Required."
	case len(text) > maxLen:
		f[field] = fmt.Sprintf("At most %d characters.", maxLen)
	case text != "":
		if a := markdown.AnalyzeComment([]byte(text)); !a.Valid() {
			f[field] = a.Issues[0].Message
		}
	}
}

// loadQuestions reads a quiz's questions in order, answer key included.
func loadQuestions(ctx context.Context, q *quizdb.Queries, id int64) ([]Question, error) {
	rows, err := q.ListQuestions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Question, len(rows))
	for i, r := range rows {
		var opts []Option
		if err := json.Unmarshal(r.Options, &opts); err != nil {
			return nil, fmt.Errorf("quiz %d question %d options: %w", id, r.ID, err)
		}
		out[i] = Question{ID: r.ID, Kind: Kind(r.Kind), Prompt: r.PromptMd, Explanation: r.ExplanationMd, Options: opts}
	}
	return out, nil
}

// publishIssues: a quiz cannot go live without questions. Their shape is checked when saved.
func (s *Service) publishIssues(ctx context.Context, tx pgx.Tx, id int64) ([]content.PublishIssue, error) {
	counts, err := quizdb.New(tx).CountQuestions(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if len(counts) == 0 || counts[0].Questions == 0 {
		return []content.PublishIssue{{Field: "questions", Code: "quiz.no_questions", Message: "Add at least one question."}}, nil
	}
	return nil, nil
}
