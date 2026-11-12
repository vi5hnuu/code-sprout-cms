package markdown

import (
	"slices"
	"testing"
)

// Embedded quizzes are collected once each, in id order, so publish can check they are live.
func TestAnalyzeCollectsQuizIDs(t *testing.T) {
	a := Analyze([]byte("Intro\n\n::quiz{id=42}\n\nMore\n\n::quiz{id=7}\n\n::quiz{id=42}\n\n::quiz{id=nope}\n"))
	if got := a.QuizIDs; !slices.Equal(got, []int64{7, 42}) {
		t.Fatalf("QuizIDs = %v, want [7 42]", got)
	}
}
