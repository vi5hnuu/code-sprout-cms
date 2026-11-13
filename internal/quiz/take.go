package quiz

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/quiz/quizdb"
)

// Taking a quiz. The public page (intro, number of questions, pass mark) is cacheable and
// crawlable; the questions are served only to signed-in readers and never carry the answer
// key; grading happens here, against the version the reader was given.

// Detail is the quiz part of the public page.
type Detail struct {
	QuestionCount int   `json:"questionCount"`
	PassPercent   int   `json:"passPercent"`
	Version       int32 `json:"version"`
}

// Page is a live quiz's public page.
type Page struct {
	content.PublicPage
	Quiz Detail `json:"quiz"`
}

// PublicOption and PublicQuestion are what a reader taking the quiz sees: no answer key, no
// explanations (those come back with the graded attempt).
type PublicOption struct {
	ID   int16  `json:"id"`
	Body string `json:"body"`
}

type PublicQuestion struct {
	ID      int64          `json:"id"`
	Kind    Kind           `json:"kind"`
	Prompt  string         `json:"prompt"`
	Options []PublicOption `json:"options"`
}

// Sheet is a quiz handed to a reader to answer.
type Sheet struct {
	QuizID      int64            `json:"quizId"`
	Version     int32            `json:"version"`
	PassPercent int              `json:"passPercent"`
	Questions   []PublicQuestion `json:"questions"`
}

// Answer is the reader's choice on one question.
type Answer struct {
	QuestionID int64   `json:"questionId"`
	OptionIDs  []int16 `json:"optionIds"`
}

// Result is one question's outcome, shown after submitting.
type Result struct {
	QuestionID       int64   `json:"questionId"`
	Correct          bool    `json:"correct"`
	Chosen           []int16 `json:"chosen"`
	CorrectOptionIDs []int16 `json:"correctOptionIds"`
	Explanation      *string `json:"explanation"`
}

// Graded is a submitted attempt.
type Graded struct {
	AttemptID   int64     `json:"attemptId"`
	Correct     int       `json:"correct"`
	Total       int       `json:"total"`
	Percent     int       `json:"percent"`
	Passed      bool      `json:"passed"`
	PassPercent int       `json:"passPercent"`
	Results     []Result  `json:"results"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Attempt is one past attempt in a reader's history.
type Attempt struct {
	ID        int64     `json:"id"`
	Correct   int       `json:"correct"`
	Total     int       `json:"total"`
	Percent   int       `json:"percent"`
	Passed    bool      `json:"passed"`
	CreatedAt time.Time `json:"createdAt"`
}

type AttemptCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

// State is a reader's standing on one quiz, for listings and embeds.
type State struct {
	QuizID      int64      `json:"quizId"`
	Attempts    int        `json:"attempts"`
	BestPercent int        `json:"bestPercent"`
	LastPercent int        `json:"lastPercent"`
	PassedAt    *time.Time `json:"passedAt"`
}

const maxStateIDs = 100

var errChanged = apperr.Conflict("quiz.changed", "This quiz was updated while you were answering. Please start again.")

// PublicPage returns a live quiz's page, or a redirect for a renamed quiz's old slug.
func (s *Service) PublicPage(ctx context.Context, locale, slug string) (*Page, *content.Redirect, error) {
	p, redirect, err := s.reader.Page(ctx, content.TypeQuiz, locale, slug)
	if err != nil || redirect != nil {
		return nil, redirect, err
	}
	q := quizdb.New(s.db.Replica)
	ext, err := q.GetQuizExt(ctx, p.ID)
	if db.IsNoRows(err) {
		return nil, nil, errNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	counts, err := q.CountQuestions(ctx, []int64{p.ID})
	if err != nil {
		return nil, nil, err
	}
	d := Detail{PassPercent: int(ext.PassPercent), Version: ext.Version}
	if len(counts) > 0 {
		d.QuestionCount = int(counts[0].Questions)
	}
	return &Page{PublicPage: *p, Quiz: d}, nil, nil
}

func (s *Service) requireLive(ctx context.Context, id int64) error {
	live, err := s.reader.LiveIDs(ctx, []int64{id})
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return errNotFound
	}
	return nil
}

// Questions hands a signed-in reader the quiz to answer, shuffled when the quiz asks for it.
// Read from the primary, so it always matches the version grading will check against.
func (s *Service) Questions(ctx context.Context, quizID int64) (Sheet, error) {
	if err := s.requireLive(ctx, quizID); err != nil {
		return Sheet{}, err
	}
	q := quizdb.New(s.db.Primary)
	ext, err := q.GetQuizExt(ctx, quizID)
	if db.IsNoRows(err) {
		return Sheet{}, errNotFound
	}
	if err != nil {
		return Sheet{}, err
	}
	qs, err := loadQuestions(ctx, q, quizID)
	if err != nil {
		return Sheet{}, err
	}
	sheet := Sheet{QuizID: quizID, Version: ext.Version, PassPercent: int(ext.PassPercent), Questions: make([]PublicQuestion, len(qs))}
	for i, qq := range qs {
		opts := make([]PublicOption, len(qq.Options))
		for j, o := range qq.Options {
			opts[j] = PublicOption{ID: o.ID, Body: o.Body}
		}
		if ext.Shuffle {
			rand.Shuffle(len(opts), func(a, b int) { opts[a], opts[b] = opts[b], opts[a] })
		}
		sheet.Questions[i] = PublicQuestion{ID: qq.ID, Kind: qq.Kind, Prompt: qq.Prompt, Options: opts}
	}
	if ext.Shuffle {
		rand.Shuffle(len(sheet.Questions), func(a, b int) { sheet.Questions[a], sheet.Questions[b] = sheet.Questions[b], sheet.Questions[a] })
	}
	return sheet, nil
}

// Submit grades an attempt and records it. A question left unanswered counts as wrong; a
// multiple-answer question is right only when exactly the correct options are chosen.
func (s *Service) Submit(ctx context.Context, p auth.Principal, quizID int64, version int32, answers []Answer) (Graded, error) {
	if err := s.requireLive(ctx, quizID); err != nil {
		return Graded{}, err
	}
	var g Graded
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := quizdb.New(tx)
		ext, err := q.GetQuizExt(ctx, quizID)
		if db.IsNoRows(err) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if ext.Version != version {
			return errChanged
		}
		qs, err := loadQuestions(ctx, q, quizID)
		if err != nil {
			return err
		}
		chosen, err := indexAnswers(qs, answers)
		if err != nil {
			return err
		}
		g = grade(qs, chosen, int(ext.PassPercent))
		raw, _ := json.Marshal(answers)
		row, err := q.InsertAttempt(ctx, quizdb.InsertAttemptParams{
			UserID: p.UserID, QuizID: quizID, Version: ext.Version, Correct: int16(g.Correct), Total: int16(g.Total),
			Percent: int16(g.Percent), Passed: g.Passed, Answers: raw,
		})
		if err != nil {
			return err
		}
		g.AttemptID, g.CreatedAt = row.ID, row.CreatedAt
		return q.UpsertUserQuiz(ctx, quizdb.UpsertUserQuizParams{UserID: p.UserID, QuizID: quizID, Percent: int16(g.Percent), Passed: g.Passed})
	})
	return g, err
}

// indexAnswers maps each answered question to its chosen options. An answer to a question
// the quiz does not have means the reader holds an older sheet; options outside the
// question, or repeats, are a malformed request.
func indexAnswers(qs []Question, answers []Answer) (map[int64][]int16, error) {
	byID := make(map[int64]Question, len(qs))
	for _, q := range qs {
		byID[q.ID] = q
	}
	out := make(map[int64][]int16, len(answers))
	for i, a := range answers {
		q, ok := byID[a.QuestionID]
		if !ok {
			return nil, errChanged
		}
		if _, dup := out[a.QuestionID]; dup {
			return nil, apperr.ValidationFailed(map[string]string{fmt.Sprintf("answers[%d]", i): "Each question is answered once."})
		}
		ids := slices.Clone(a.OptionIDs)
		slices.Sort(ids)
		if len(slices.Compact(ids)) != len(a.OptionIDs) || (q.Kind == KindSingle && len(ids) > 1) {
			return nil, apperr.ValidationFailed(map[string]string{fmt.Sprintf("answers[%d]", i): "Choose each option at most once (one for single-answer questions)."})
		}
		for _, id := range ids {
			if !slices.ContainsFunc(q.Options, func(o Option) bool { return o.ID == id }) {
				return nil, apperr.ValidationFailed(map[string]string{fmt.Sprintf("answers[%d]", i): "Unknown option."})
			}
		}
		out[a.QuestionID] = ids
	}
	return out, nil
}

// grade scores answers against the key, in the quiz's own question order.
func grade(qs []Question, chosen map[int64][]int16, passPercent int) Graded {
	g := Graded{Total: len(qs), PassPercent: passPercent, Results: make([]Result, len(qs))}
	for i, q := range qs {
		var key []int16
		for _, o := range q.Options {
			if o.Correct {
				key = append(key, o.ID)
			}
		}
		pick := chosen[q.ID]
		if pick == nil {
			pick = []int16{}
		}
		ok := len(pick) > 0 && slices.Equal(pick, key) // both sorted by id
		if ok {
			g.Correct++
		}
		g.Results[i] = Result{QuestionID: q.ID, Correct: ok, Chosen: pick, CorrectOptionIDs: key, Explanation: q.Explanation}
	}
	if g.Total > 0 {
		g.Percent = g.Correct * 100 / g.Total
	}
	g.Passed = g.Percent >= passPercent
	return g
}

// Attempts lists a reader's attempts at one quiz, newest first.
func (s *Service) Attempts(ctx context.Context, p auth.Principal, quizID int64, req page.Request[AttemptCursor]) (page.Page[Attempt], error) {
	params := quizdb.ListAttemptsParams{UserID: p.UserID, QuizID: quizID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := quizdb.New(s.db.Primary).ListAttempts(ctx, params)
	if err != nil {
		return page.Page[Attempt]{}, err
	}
	out := make([]Attempt, len(rows))
	for i, r := range rows {
		out[i] = Attempt{ID: r.ID, Correct: int(r.Correct), Total: int(r.Total), Percent: int(r.Percent), Passed: r.Passed, CreatedAt: r.CreatedAt}
	}
	return page.Build(out, req.Limit, func(a Attempt) AttemptCursor { return AttemptCursor{At: a.CreatedAt, ID: a.ID} }), nil
}

// States returns the reader's standing on up to 100 quizzes; quizzes never attempted are absent.
func (s *Service) States(ctx context.Context, p auth.Principal, ids []int64) ([]State, error) {
	if len(ids) > maxStateIDs {
		return nil, apperr.ValidationFailed(map[string]string{"ids": fmt.Sprintf("At most %d ids.", maxStateIDs)})
	}
	out := []State{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := quizdb.New(s.db.Primary).UserQuizStates(ctx, quizdb.UserQuizStatesParams{UserID: p.UserID, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, State{QuizID: r.QuizID, Attempts: int(r.Attempts), BestPercent: int(r.BestPercent), LastPercent: int(r.LastPercent), PassedAt: r.FirstPassedAt})
	}
	return out, nil
}
