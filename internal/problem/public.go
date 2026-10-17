package problem

import (
	"context"
	"time"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/problem/problemdb"
)

// Page returns a live problem's statement with its samples, starter code and stats, or a
// redirect when slug is a renamed problem's old slug.
func (s *Service) Page(ctx context.Context, locale, slug string) (*Page, *content.Redirect, error) {
	p, redirect, err := s.reader.Page(ctx, content.TypeProblem, locale, slug)
	if err != nil || redirect != nil {
		return nil, redirect, err
	}
	q := problemdb.New(s.db.Replica)
	ext, err := q.GetProblemExt(ctx, p.ID)
	if db.IsNoRows(err) {
		return nil, nil, errNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	d := Detail{
		Difficulty: Difficulty(ext.Difficulty), TimeLimitMs: ext.TimeLimitMs, MemoryMB: ext.MemoryMb,
		EditorialID: ext.EditorialContentID, SubmissionCount: ext.SubmissionCount, AcceptedCount: ext.AcceptedCount,
		AcceptanceRate: acceptance(ext.SubmissionCount, ext.AcceptedCount),
	}
	if d.Samples, err = s.samples(ctx, q, p.ID); err != nil {
		return nil, nil, err
	}
	templates, err := q.ListTemplates(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	d.Templates = make([]PublicTemplate, len(templates))
	for i, t := range templates {
		d.Templates[i] = PublicTemplate{Language: t.Language, StarterCode: t.StarterCode}
	}
	hints, err := s.hints(ctx, q, p.ID)
	if err != nil {
		return nil, nil, err
	}
	d.HintCount = len(hints)
	return &Page{PublicPage: *p, Problem: d}, nil, nil
}

// Hints are served separately so the statement page does not ship them; readers reveal them
// one at a time on the client.
func (s *Service) Hints(ctx context.Context, problemID int64) ([]string, error) {
	live, err := s.reader.LiveIDs(ctx, []int64{problemID})
	if err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, errNotFound
	}
	return s.hints(ctx, problemdb.New(s.db.Replica), problemID)
}

type ListFilter struct {
	Difficulty *Difficulty
	Tag        string // kind:slug
}

// List returns live public problems, newest first. Difficulty alone is served by its own index;
// a tag listing filtered by difficulty may return pages shorter than the limit (the cursor
// still advances correctly).
func (s *Service) List(ctx context.Context, f ListFilter, req page.Request[content.PublishedCursor]) (page.Page[ListItem], error) {
	if f.Difficulty != nil && !f.Difficulty.valid() {
		return page.Page[ListItem]{}, apperr.ValidationFailed(map[string]string{"difficulty": "difficulty must be EASY, MEDIUM or HARD."})
	}
	var items []content.ListItem
	var next *string
	if f.Tag != "" {
		t := content.TypeProblem
		res, err := s.reader.List(ctx, content.ListFilter{Type: &t, Tag: f.Tag}, req)
		if err != nil {
			return page.Page[ListItem]{}, err
		}
		items, next = res.Items, res.NextCursor
	} else {
		params := problemdb.ListListedProblemsParams{RowLimit: int32(req.Limit + 1)}
		if f.Difficulty != nil {
			d := string(*f.Difficulty)
			params.Difficulty = &d
		}
		if req.Key != nil {
			params.AfterListed, params.AfterID = &req.Key.PublishedAt, &req.Key.ID
		}
		rows, err := problemdb.New(s.db.Replica).ListListedProblems(ctx, params)
		if err != nil {
			return page.Page[ListItem]{}, err
		}
		ids := make([]int64, len(rows))
		listedAt := make(map[int64]time.Time, len(rows))
		for i, r := range rows {
			ids[i] = r.ContentID
			listedAt[r.ContentID] = *r.ListedAt
		}
		built := page.Build(ids, req.Limit, func(id int64) content.PublishedCursor {
			return content.PublishedCursor{PublishedAt: listedAt[id], ID: id}
		})
		next = built.NextCursor
		if items, err = s.reader.ItemsByID(ctx, built.Items); err != nil {
			return page.Page[ListItem]{}, err
		}
	}

	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	exts, err := problemdb.New(s.db.Replica).ListProblemExt(ctx, ids)
	if err != nil {
		return page.Page[ListItem]{}, err
	}
	byID := make(map[int64]problemdb.ProblemExt, len(exts))
	for _, e := range exts {
		byID[e.ContentID] = e
	}
	out := make([]ListItem, 0, len(items))
	for _, it := range items {
		e, ok := byID[it.ID]
		if !ok || (f.Difficulty != nil && Difficulty(e.Difficulty) != *f.Difficulty) {
			continue
		}
		out = append(out, ListItem{ListItem: it, Difficulty: Difficulty(e.Difficulty), AcceptanceRate: acceptance(e.SubmissionCount, e.AcceptedCount)})
	}
	return page.Page[ListItem]{Items: out, NextCursor: next}, nil
}

// Today returns the problem of the day (UTC), or 404 if none is scheduled.
func (s *Service) Today(ctx context.Context) (ListItem, error) {
	day := time.Now().UTC().Truncate(24 * time.Hour)
	potd, err := problemdb.New(s.db.Replica).GetProblemOfDay(ctx, day)
	if db.IsNoRows(err) {
		return ListItem{}, apperr.NotFound("problem.no_problem_of_day", "No problem of the day today.")
	}
	if err != nil {
		return ListItem{}, err
	}
	live, err := s.reader.ItemsByID(ctx, []int64{potd.ProblemID})
	if err != nil {
		return ListItem{}, err
	}
	if len(live) == 0 {
		return ListItem{}, apperr.NotFound("problem.no_problem_of_day", "No problem of the day today.")
	}
	ext, err := problemdb.New(s.db.Replica).GetProblemExt(ctx, potd.ProblemID)
	if err != nil {
		return ListItem{}, err
	}
	return ListItem{ListItem: live[0], Difficulty: Difficulty(ext.Difficulty), AcceptanceRate: acceptance(ext.SubmissionCount, ext.AcceptedCount)}, nil
}
