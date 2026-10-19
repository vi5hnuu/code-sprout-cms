// Package arenatest builds a working arena (content, problems, storage) against the test
// database and storage, and publishes problems through the real workflow, for tests in the
// problem and submission packages. Test-only; nothing in production imports it.
package arenatest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/storage"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

type Env struct {
	T        *testing.T
	DB       *db.DB
	Assets   *asset.Service
	Contents *content.Service
	Reader   *content.Reader
	Problems *problem.Service
	Category taxonomy.Category
	Author   auth.Principal
	Editor   auth.Principal
}

func New(t *testing.T) *Env {
	t.Helper()
	d := testenv.DB(t)
	store := storage.NewS3(storage.Config{Endpoint: testenv.StorageEndpoint(t), Region: "us-east-1", AccessKey: "sprout", SecretKey: "sprout-secret"})
	assets := asset.NewService(d, store, asset.Config{PublicBucket: "sprout-public", PrivateBucket: "sprout-private", PublicBaseURL: "http://localhost:9010/sprout-public"})
	policy := access.NewPolicy(d.Primary)
	tax := taxonomy.NewService(d, policy)
	contents := content.NewService(d, policy, assets, tax)
	users := user.NewService(d.Primary, d.Replica, assets)
	reader := content.NewReader(d, assets, tax, users, engagement.NewStatsReader(d))
	e := &Env{T: t, DB: d, Assets: assets, Contents: contents, Reader: reader,
		Problems: problem.NewService(d, contents, reader, assets, policy)}

	admin := testenv.Admin(t, d)
	cat, err := tax.Create(context.Background(), admin, taxonomy.CreateCategory{Slug: testenv.Unique("arena"), Name: "Arena"})
	if err != nil {
		t.Fatal(err)
	}
	e.Category = cat
	e.Author = testenv.User(t, d)
	e.Editor = testenv.User(t, d)
	testenv.Grant(t, d, e.Editor, "EDITOR", &cat.ID)
	return e
}

// UploadTestSet uploads cases as an NDJSON TEST_SET file owned by p and returns its id.
func (e *Env) UploadTestSet(p auth.Principal, cases []judge.TestCase) int64 {
	e.T.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, c := range cases {
		if err := enc.Encode(map[string]string{"input": c.Stdin, "output": c.Expected}); err != nil {
			e.T.Fatal(err)
		}
	}
	// Identical bytes are deduplicated per owner, which is fine: test sets are immutable.
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	ctx := context.Background()
	ticket, err := e.Assets.CreateUpload(ctx, p, asset.CreateUpload{
		Purpose: asset.PurposeTestSet, Mime: "application/x-ndjson", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		e.T.Fatal(err)
	}
	if ticket.Upload != nil {
		req, _ := http.NewRequest(ticket.Upload.Method, ticket.Upload.URL, bytes.NewReader(body))
		for k, v := range ticket.Upload.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			e.T.Fatal(err)
		}
		resp.Body.Close()
		if _, err := e.Assets.Complete(ctx, p, ticket.File.ID); err != nil {
			e.T.Fatal(err)
		}
	}
	return ticket.File.ID
}

// Draft creates a problem draft (statement + limits) by the author, with optional tags.
func (e *Env) Draft(slug string, tagIDs ...int64) content.Draft {
	e.T.Helper()
	summary := "Return the sum of two integers read from standard input, a warm-up for the arena."
	d, err := e.Problems.Create(context.Background(), e.Author, problem.CreateInput{
		DraftInput: content.DraftInput{
			Meta: content.Meta{Title: "Sum " + slug, Slug: slug, Summary: &summary, CategoryID: &e.Category.ID, TagIDs: tagIDs},
			Body: "## Task\n\nRead two integers and print their sum.\n",
		},
		Difficulty: problem.Easy, TimeLimitMs: 1000, MemoryMB: 128,
	})
	if err != nil {
		e.T.Fatal(err)
	}
	return d
}

// Publish sets up samples, a template and the given hidden cases, then publishes the problem
// (with optional tags).
func (e *Env) Publish(slug string, samples []problem.Sample, hidden []judge.TestCase, driver *string, tagIDs ...int64) int64 {
	e.T.Helper()
	ctx := context.Background()
	d := e.Draft(slug, tagIDs...)
	if _, err := e.Problems.ReplaceSamples(ctx, e.Author, d.ID, samples); err != nil {
		e.T.Fatal(err)
	}
	if _, err := e.Problems.PutTemplate(ctx, e.Author, d.ID, problem.Template{Language: "python", StarterCode: "# your code", DriverCode: driver}); err != nil {
		e.T.Fatal(err)
	}
	if _, err := e.Problems.AddTestSet(ctx, e.Author, d.ID, e.UploadTestSet(e.Author, hidden)); err != nil {
		e.T.Fatal(err)
	}
	if _, err := e.Contents.Publish(ctx, e.Editor, d.ID); err != nil {
		e.T.Fatal(err)
	}
	return d.ID
}

// PublishArticle publishes an article in the arena category (e.g. a problem's editorial) and
// returns its id.
func (e *Env) PublishArticle(slug string) int64 {
	e.T.Helper()
	ctx := context.Background()
	summary := "How to solve the arena problem step by step, with the reasoning behind each choice."
	d, err := e.Contents.Create(ctx, e.Author, content.CreateInput{Type: content.TypeArticle, DraftInput: content.DraftInput{
		Meta: content.Meta{Title: "Editorial " + slug, Slug: slug, Summary: &summary, CategoryID: &e.Category.ID},
		Body: "## Approach\n\nAdd the two numbers.\n",
	}})
	if err != nil {
		e.T.Fatal(err)
	}
	if _, err := e.Contents.Publish(ctx, e.Editor, d.ID); err != nil {
		e.T.Fatal(err)
	}
	return d.ID
}
