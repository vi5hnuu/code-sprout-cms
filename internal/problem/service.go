package problem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/langs"
	"code-sprout-cms/internal/problem/problemdb"
)

// Files is what problems need from asset storage (implemented by asset.Service).
type Files interface {
	Stored(ctx context.Context, id int64) (asset.Stored, error)
	Open(ctx context.Context, id int64) (io.ReadCloser, error)
}

type Service struct {
	db       *db.DB
	contents *content.Service
	reader   *content.Reader
	files    Files
	policy   *access.Policy
}

// NewService also registers the PROBLEM type's lifecycle hooks with the content service.
func NewService(d *db.DB, contents *content.Service, reader *content.Reader, files Files, policy *access.Policy) *Service {
	s := &Service{db: d, contents: contents, reader: reader, files: files, policy: policy}
	contents.RegisterType(content.TypeProblem, content.TypeHooks{
		Check:       s.publishIssues,
		OnPublish:   s.onPublish,
		OnUnpublish: s.onUnpublish,
	})
	return s
}

var errNotFound = apperr.NotFound("problem.not_found", "No such problem.")

func validateLimits(f map[string]string, d Difficulty, timeMs, memMB int) {
	if !d.valid() {
		f["difficulty"] = "difficulty must be EASY, MEDIUM or HARD."
	}
	if timeMs < 100 || timeMs > 30_000 {
		f["timeLimitMs"] = "Time limit must be 100-30000 ms."
	}
	if memMB < 16 || memMB > 1024 {
		f["memoryMb"] = "Memory limit must be 16-1024 MB."
	}
}

// Create starts a problem: a PROBLEM content item (the statement, edited and reviewed like any
// article) plus its judging setup.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (content.Draft, error) {
	if in.TimeLimitMs == 0 {
		in.TimeLimitMs = 2000
	}
	if in.MemoryMB == 0 {
		in.MemoryMB = 256
	}
	f := map[string]string{}
	validateLimits(f, in.Difficulty, in.TimeLimitMs, in.MemoryMB)
	if len(f) > 0 {
		return content.Draft{}, apperr.ValidationFailed(f)
	}
	return s.contents.CreateTyped(ctx, p, content.CreateInput{Type: content.TypeProblem, DraftInput: in.DraftInput},
		func(ctx context.Context, tx pgx.Tx, id int64) error {
			return problemdb.New(tx).InsertProblemExt(ctx, problemdb.InsertProblemExtParams{
				ContentID: id, Difficulty: string(in.Difficulty), TimeLimitMs: int32(in.TimeLimitMs), MemoryMb: int32(in.MemoryMB),
			})
		})
}

func (s *Service) Studio(ctx context.Context, p auth.Principal, id int64) (StudioView, error) {
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	q := problemdb.New(s.db.Primary)
	ext, err := q.GetProblemExt(ctx, id)
	if db.IsNoRows(err) {
		return StudioView{}, errNotFound
	}
	if err != nil {
		return StudioView{}, err
	}
	v := StudioView{ContentID: id, Settings: Settings{
		Difficulty: Difficulty(ext.Difficulty), TimeLimitMs: int(ext.TimeLimitMs), MemoryMB: int(ext.MemoryMb),
		Checker: ext.Checker, CheckerFileID: ext.CheckerFileID, EditorialContentID: ext.EditorialContentID,
		VideoContentID: ext.VideoContentID,
	}}
	if v.Samples, err = s.samples(ctx, q, id); err != nil {
		return StudioView{}, err
	}
	templates, err := q.ListTemplates(ctx, id)
	if err != nil {
		return StudioView{}, err
	}
	v.Templates = make([]Template, len(templates))
	for i, t := range templates {
		v.Templates[i] = Template{Language: t.Language, StarterCode: t.StarterCode, DriverCode: t.DriverCode}
	}
	if v.Hints, err = s.hints(ctx, q, id); err != nil {
		return StudioView{}, err
	}
	sets, err := q.ListTestSets(ctx, id)
	if err != nil {
		return StudioView{}, err
	}
	v.TestSets = make([]TestSet, len(sets))
	for i, t := range sets {
		v.TestSets[i] = TestSet{ID: t.ID, Version: t.Version, FileID: t.FileID, CaseCount: t.CaseCount, Bytes: t.TotalBytes,
			Active: ext.ActiveTestSetID != nil && *ext.ActiveTestSetID == t.ID, CreatedAt: t.CreatedAt}
	}
	return v, nil
}

func (s *Service) UpdateSettings(ctx context.Context, p auth.Principal, id int64, in Settings) (StudioView, error) {
	f := map[string]string{}
	validateLimits(f, in.Difficulty, in.TimeLimitMs, in.MemoryMB)
	switch in.Checker {
	case "", "EXACT", "TOKEN", "FLOAT":
		in.CheckerFileID = nil
	case "CUSTOM":
		// A custom checker program needs judge support that ls-judge does not have yet.
		f["checker"] = "Custom checkers are not supported yet."
	default:
		f["checker"] = "checker must be EXACT, TOKEN or FLOAT."
	}
	if in.Checker == "" {
		in.Checker = "EXACT"
	}
	if len(f) > 0 {
		return StudioView{}, apperr.ValidationFailed(f)
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	if err := s.validateLinkTargets(ctx, in); err != nil {
		return StudioView{}, err
	}
	if err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := problemdb.New(tx)
		before, err := q.GetProblemExt(ctx, id)
		if err != nil {
			return err
		}
		if err := q.UpdateProblemSettings(ctx, problemdb.UpdateProblemSettingsParams{
			ContentID: id, Difficulty: string(in.Difficulty), TimeLimitMs: int32(in.TimeLimitMs), MemoryMb: int32(in.MemoryMB),
			Checker: in.Checker, CheckerFileID: in.CheckerFileID, EditorialContentID: in.EditorialContentID, VideoContentID: in.VideoContentID,
		}); err != nil {
			return err
		}
		// The new editorial and video show at once on a live problem, like its difficulty.
		cleared := before.VideoContentID != nil && in.VideoContentID == nil
		if err := writeLinks(ctx, tx, id, in.EditorialContentID, in.VideoContentID, cleared); err != nil {
			return err
		}
		if live, err := s.reader.LiveIDs(ctx, []int64{id}); err != nil {
			return err
		} else if len(live) > 0 {
			if err := content.Touched(ctx, tx, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "problem.settings", Entity: "content", EntityID: id, Data: in})
	}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

// ReplaceSamples sets the examples shown in the statement (and used by "Run").
func (s *Service) ReplaceSamples(ctx context.Context, p auth.Principal, id int64, samples []Sample) (StudioView, error) {
	if len(samples) > maxSamples {
		return StudioView{}, apperr.ValidationFailed(map[string]string{"samples": fmt.Sprintf("At most %d samples.", maxSamples)})
	}
	for i, sm := range samples {
		if len(sm.Input) > maxSampleBytes || len(sm.Output) > maxSampleBytes {
			return StudioView{}, apperr.ValidationFailed(map[string]string{fmt.Sprintf("samples[%d]", i): "Samples are at most 16 KiB each way."})
		}
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	if err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := problemdb.New(tx)
		if err := q.DeleteSamples(ctx, id); err != nil {
			return err
		}
		for i, sm := range samples {
			if err := q.InsertSample(ctx, problemdb.InsertSampleParams{
				ProblemID: id, Position: int32(i + 1), Input: sm.Input, Output: sm.Output, Explanation: sm.Explanation,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

func (s *Service) PutTemplate(ctx context.Context, p auth.Principal, id int64, t Template) (StudioView, error) {
	f := map[string]string{}
	if !langs.Runnable(t.Language) {
		f["language"] = "Unsupported language; use one of " + langs.List() + "."
	}
	if len(t.StarterCode) > maxTemplateCode || (t.DriverCode != nil && len(*t.DriverCode) > maxTemplateCode) {
		f["starterCode"] = "Templates are at most 64 KiB."
	}
	if t.DriverCode != nil && strings.TrimSpace(*t.DriverCode) == "" {
		t.DriverCode = nil
	}
	if t.DriverCode != nil && strings.Count(*t.DriverCode, UserCodePlaceholder) != 1 {
		f["driverCode"] = "A driver must contain " + UserCodePlaceholder + " exactly once."
	}
	if len(f) > 0 {
		return StudioView{}, apperr.ValidationFailed(f)
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	if err := problemdb.New(s.db.Primary).UpsertTemplate(ctx, problemdb.UpsertTemplateParams{
		ProblemID: id, Language: t.Language, StarterCode: t.StarterCode, DriverCode: t.DriverCode,
	}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

func (s *Service) DeleteTemplate(ctx context.Context, p auth.Principal, id int64, language string) (StudioView, error) {
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	if _, err := problemdb.New(s.db.Primary).DeleteTemplate(ctx, problemdb.DeleteTemplateParams{ProblemID: id, Language: language}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

func (s *Service) ReplaceHints(ctx context.Context, p auth.Principal, id int64, hints []string) (StudioView, error) {
	if len(hints) > maxHints {
		return StudioView{}, apperr.ValidationFailed(map[string]string{"hints": fmt.Sprintf("At most %d hints.", maxHints)})
	}
	clean := make([]string, 0, len(hints))
	for i, h := range hints {
		h = strings.TrimSpace(h)
		if h == "" || len(h) > maxHintBytes {
			return StudioView{}, apperr.ValidationFailed(map[string]string{fmt.Sprintf("hints[%d]", i): "Hints are 1 byte to 8 KiB."})
		}
		clean = append(clean, h)
	}
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	if err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := problemdb.New(tx)
		if err := q.DeleteHints(ctx, id); err != nil {
			return err
		}
		return q.InsertHints(ctx, problemdb.InsertHintsParams{ProblemID: id, Bodies: clean})
	}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

// AddTestSet registers an uploaded NDJSON file as the problem's next test set version and makes
// it active. The file is validated in full first, so a malformed set never reaches the judge.
func (s *Service) AddTestSet(ctx context.Context, p auth.Principal, id, fileID int64) (StudioView, error) {
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	f, err := s.files.Stored(ctx, fileID)
	if err != nil {
		return StudioView{}, err
	}
	if f.Purpose != asset.PurposeTestSet || f.Mime != "application/x-ndjson" {
		return StudioView{}, apperr.ValidationFailed(map[string]string{"fileId": "Upload the tests as an NDJSON TEST_SET file."})
	}
	if !p.IsAdmin && (f.OwnerID == nil || *f.OwnerID != p.UserID) {
		return StudioView{}, apperr.ValidationFailed(map[string]string{"fileId": "Use a test file you uploaded."})
	}
	r, err := s.files.Open(ctx, fileID)
	if err != nil {
		return StudioView{}, err
	}
	_, count, err := ReadTestSet(r, false)
	r.Close()
	if errors.Is(err, ErrBadTestSet) {
		return StudioView{}, apperr.Invalid("problem.bad_test_set", err.Error())
	}
	if err != nil {
		return StudioView{}, err
	}
	if err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := problemdb.New(tx)
		if _, err := q.GetProblemExt(ctx, id); err != nil {
			return err
		}
		version, err := q.NextTestSetVersion(ctx, id)
		if err != nil {
			return err
		}
		ts, err := q.InsertTestSet(ctx, problemdb.InsertTestSetParams{
			ProblemID: id, Version: version, FileID: fileID, CaseCount: int32(count), TotalBytes: f.SizeBytes,
			Sha256: f.SHA256, CreatedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		if err := q.SetActiveTestSet(ctx, problemdb.SetActiveTestSetParams{ProblemID: id, TestSetID: &ts.ID}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "problem.test_set", Entity: "content", EntityID: id,
			Data: map[string]any{"version": version, "cases": count}})
	}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

// ActivateTestSet switches back to an earlier version (e.g. after a bad upload).
func (s *Service) ActivateTestSet(ctx context.Context, p auth.Principal, id int64, version int32) (StudioView, error) {
	if err := s.contents.RequireStructureEdit(ctx, p, id, content.TypeProblem); err != nil {
		return StudioView{}, err
	}
	q := problemdb.New(s.db.Primary)
	ts, err := q.GetTestSetByVersion(ctx, problemdb.GetTestSetByVersionParams{ProblemID: id, Version: version})
	if db.IsNoRows(err) {
		return StudioView{}, apperr.NotFound("problem.test_set_not_found", "No such test set version.")
	}
	if err != nil {
		return StudioView{}, err
	}
	if err := q.SetActiveTestSet(ctx, problemdb.SetActiveTestSetParams{ProblemID: id, TestSetID: &ts.ID}); err != nil {
		return StudioView{}, err
	}
	return s.Studio(ctx, p, id)
}

// ScheduleOfDay sets the problem of the day (site-wide EDITOR).
func (s *Service) ScheduleOfDay(ctx context.Context, p auth.Principal, day time.Time, problemID int64) error {
	if err := s.policy.Require(ctx, p, access.RoleEditor, nil); err != nil {
		return err
	}
	live, err := s.reader.LiveIDs(ctx, []int64{problemID})
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return apperr.ValidationFailed(map[string]string{"problemId": "Choose a published problem."})
	}
	if _, err := problemdb.New(s.db.Primary).GetProblemExt(ctx, problemID); db.IsNoRows(err) {
		return apperr.ValidationFailed(map[string]string{"problemId": "That item is not a problem."})
	} else if err != nil {
		return err
	}
	d := day.UTC().Truncate(24 * time.Hour)
	return problemdb.New(s.db.Primary).UpsertProblemOfDay(ctx, problemdb.UpsertProblemOfDayParams{Day: d, ProblemID: problemID, ScheduledBy: &p.UserID})
}

// ── lifecycle hooks ──────────────────────────────────────────────────────────

// publishIssues: a problem cannot go live until it can be solved and judged.
func (s *Service) publishIssues(ctx context.Context, tx pgx.Tx, id int64) ([]content.PublishIssue, error) {
	q := problemdb.New(tx)
	ext, err := q.GetProblemExt(ctx, id)
	if err != nil {
		return nil, err
	}
	var issues []content.PublishIssue
	if ext.ActiveTestSetID == nil {
		issues = append(issues, content.PublishIssue{Field: "testSets", Code: "problem.no_tests", Message: "Upload a test set."})
	}
	samples, err := q.ListSamples(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		issues = append(issues, content.PublishIssue{Field: "samples", Code: "problem.no_samples", Message: "Add at least one sample."})
	}
	templates, err := q.ListTemplates(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(templates) == 0 {
		issues = append(issues, content.PublishIssue{Field: "templates", Code: "problem.no_templates", Message: "Add starter code for at least one language."})
	}
	return issues, nil
}

// onPublish keeps the problem listings in step: only live PUBLIC problems are listed.
func (s *Service) onPublish(ctx context.Context, tx pgx.Tx, live content.LiveInfo) error {
	var listedAt *time.Time
	if live.Visibility == content.VisibilityPublic {
		listedAt = &live.PublishedAt
	}
	q := problemdb.New(tx)
	if err := q.SetListedAt(ctx, problemdb.SetListedAtParams{ContentID: live.ID, ListedAt: listedAt}); err != nil {
		return err
	}
	ext, err := q.GetProblemExt(ctx, live.ID)
	if err != nil {
		return err
	}
	return writeLinks(ctx, tx, live.ID, ext.EditorialContentID, ext.VideoContentID, false)
}

// writeLinks turns the problem's editorial and video settings into the page links readers see.
// A chosen video replaces any automatic one; with none chosen, an automatic link (written by
// the video sync) stays, unless the editor just cleared their choice (the sync re-links later).
func writeLinks(ctx context.Context, tx pgx.Tx, id int64, editorial, video *int64, videoCleared bool) error {
	if err := content.SetLinks(ctx, tx, id, "EDITORIAL", idsOf(editorial)); err != nil {
		return err
	}
	if video != nil || videoCleared {
		return content.SetLinks(ctx, tx, id, "VIDEO", idsOf(video))
	}
	return nil
}

func idsOf(id *int64) []int64 {
	if id == nil {
		return nil
	}
	return []int64{*id}
}

// validateLinkTargets checks the editorial is a live page (not a problem) and the video a
// live VIDEO, so a typo never links readers to nothing.
func (s *Service) validateLinkTargets(ctx context.Context, in Settings) error {
	var ids []int64
	ids = append(ids, idsOf(in.EditorialContentID)...)
	ids = append(ids, idsOf(in.VideoContentID)...)
	if len(ids) == 0 {
		return nil
	}
	items, err := s.reader.ItemsByID(ctx, ids)
	if err != nil {
		return err
	}
	types := make(map[int64]content.Type, len(items))
	for _, it := range items {
		types[it.ID] = it.Type
	}
	f := map[string]string{}
	if in.EditorialContentID != nil {
		if t, ok := types[*in.EditorialContentID]; !ok || t == content.TypeProblem || t == content.TypeVideo {
			f["editorialContentId"] = "Choose a live article or guide."
		}
	}
	if in.VideoContentID != nil {
		if t, ok := types[*in.VideoContentID]; !ok || t != content.TypeVideo {
			f["videoContentId"] = "Choose a live video."
		}
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	return nil
}

func (s *Service) onUnpublish(ctx context.Context, tx pgx.Tx, id int64) error {
	return problemdb.New(tx).SetListedAt(ctx, problemdb.SetListedAtParams{ContentID: id})
}

// ── shared reads ─────────────────────────────────────────────────────────────

func (s *Service) samples(ctx context.Context, q *problemdb.Queries, id int64) ([]Sample, error) {
	rows, err := q.ListSamples(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, len(rows))
	for i, r := range rows {
		out[i] = Sample{Input: r.Input, Output: r.Output, Explanation: r.Explanation}
	}
	return out, nil
}

func (s *Service) hints(ctx context.Context, q *problemdb.Queries, id int64) ([]string, error) {
	rows, err := q.ListHints(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.BodyMd
	}
	return out, nil
}

// Judging returns what the submission runner needs for a live problem.
func (s *Service) Judging(ctx context.Context, id int64) (JudgeSpec, error) {
	q := problemdb.New(s.db.Primary)
	ext, err := q.GetProblemExt(ctx, id)
	if db.IsNoRows(err) {
		return JudgeSpec{}, errNotFound
	}
	if err != nil {
		return JudgeSpec{}, err
	}
	spec := JudgeSpec{ProblemID: id, TimeLimitMs: int(ext.TimeLimitMs), MemoryMB: int(ext.MemoryMb), Drivers: map[string]string{}}
	if spec.Samples, err = s.samples(ctx, q, id); err != nil {
		return JudgeSpec{}, err
	}
	templates, err := q.ListTemplates(ctx, id)
	if err != nil {
		return JudgeSpec{}, err
	}
	for _, t := range templates {
		if t.DriverCode != nil {
			spec.Drivers[t.Language] = *t.DriverCode
		}
	}
	if ext.ActiveTestSetID != nil {
		ts, err := q.GetTestSet(ctx, *ext.ActiveTestSetID)
		if err != nil {
			return JudgeSpec{}, err
		}
		spec.TestSet = &TestSetRef{ID: ts.ID, FileID: ts.FileID, SHA256: ts.Sha256, Cases: ts.CaseCount}
	}
	return spec, nil
}

// RecordResult bumps the problem's acceptance counters (called as a submission finalises).
func RecordResult(ctx context.Context, tx pgx.Tx, problemID int64, accepted bool) error {
	var a int64
	if accepted {
		a = 1
	}
	return problemdb.New(tx).RecordProblemSubmission(ctx, problemdb.RecordProblemSubmissionParams{ContentID: problemID, Accepted: a})
}

// OpenTestSet streams a test set's file for the runner.
func (s *Service) OpenTestSet(ctx context.Context, ref TestSetRef) (io.ReadCloser, error) {
	return s.files.Open(ctx, ref.FileID)
}

// Difficulties batch-loads difficulty for problem ids (missing ids are absent).
func (s *Service) Difficulties(ctx context.Context, ids []int64) (map[int64]Difficulty, error) {
	out := make(map[int64]Difficulty, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := problemdb.New(s.db.Replica).ListProblemExt(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ContentID] = Difficulty(r.Difficulty)
	}
	return out, nil
}
