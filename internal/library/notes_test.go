package library_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/library"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
)

func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// Notes are private, saving an empty note deletes it, and only live pages accept one.
func TestNotesArePrivateBoundedAndOnLivePagesOnly(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tag := testenv.Unique("notes")
	pageID := e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)
	notes := library.NewNotes(e.DB, e.Reader)
	owner, other := testenv.User(t, e.DB), testenv.User(t, e.DB)

	if _, err := notes.Put(ctx, owner, pageID, "use two pointers"); err != nil {
		t.Fatal(err)
	}
	got, err := notes.Get(ctx, owner, pageID)
	if err != nil || got.Body != "use two pointers" {
		t.Fatalf("owner reads %q, %v", got.Body, err)
	}
	if _, err := notes.Get(ctx, other, pageID); code(err) != "note.not_found" {
		t.Errorf("another reader got %v, want note.not_found", err)
	}
	if _, err := notes.Put(ctx, owner, pageID, strings.Repeat("x", 64<<10+1)); code(err) != "validation_failed" {
		t.Errorf("oversized note: %v, want validation_failed", err)
	}
	if _, err := notes.Put(ctx, owner, 1<<46, "orphan"); code(err) != "content.not_found" {
		t.Errorf("note on a missing page: %v, want content.not_found", err)
	}
	if n, err := notes.Put(ctx, owner, pageID, "   "); err != nil || n != nil {
		t.Fatalf("empty save = %v, %v; want a delete", n, err)
	}
	if _, err := notes.Get(ctx, owner, pageID); code(err) != "note.not_found" {
		t.Errorf("after an empty save: %v, want note.not_found", err)
	}
}
