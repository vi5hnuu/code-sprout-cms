package judge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer mimics ls-judge's wire format: {"error":{"code","message"}} envelopes, the 207
// batch shape and X-API-Key auth.
func fakeServer(t *testing.T) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var keys, languages []string
	mux := http.NewServeMux()
	writeErr := func(w http.ResponseWriter, status int, code, msg string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	}
	mux.HandleFunc("POST /execute", func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		var req ExecuteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		languages = append(languages, req.Language)
		switch req.Language {
		case "cobol":
			writeErr(w, 400, "invalid_request", "unknown language: cobol")
		case "quota":
			writeErr(w, 402, "quota_exhausted", "quota exhausted")
		case "boom":
			w.WriteHeader(500)
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"job_id": "j-1"})
		}
	})
	mux.HandleFunc("POST /batch/execute", func(w http.ResponseWriter, r *http.Request) {
		// Same contract as ls-judge's handleBatchExecute: the body is a bare array of jobs.
		var jobs []ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil {
			writeErr(w, 400, "invalid_request", "body must be a JSON array of jobs")
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		resp := map[string]any{"accepted": []map[string]any{}, "errors": []map[string]any{}}
		for i, j := range jobs {
			languages = append(languages, j.Language)
			if j.Language == "cobol" {
				resp["errors"] = append(resp["errors"].([]map[string]any), map[string]any{"index": i, "error": "unknown language"})
				continue
			}
			resp["accepted"] = append(resp["accepted"].([]map[string]any), map[string]any{"index": i, "job_id": "b-" + string(rune('a'+i))})
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "j-1" {
			writeErr(w, 404, "not_found", "job not found")
			return
		}
		_, _ = w.Write([]byte(`{"job_id":"j-1","status":"done","verdict":"AC","results":[{"index":0,"verdict":"AC","stdout":"3","time_ms":4}]}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "secret" {
			writeErr(w, 401, "unauthorized", "invalid API key")
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &keys, &languages
}

func TestClient(t *testing.T) {
	srv, keys, _ := fakeServer(t)
	c := New(srv.URL, "secret", nil)
	ctx := context.Background()

	id, err := c.Execute(ctx, ExecuteRequest{Language: "python", Code: "print(3)"}, "k1")
	if err != nil || id != "j-1" || (*keys)[0] != "k1" {
		t.Fatalf("execute = %q, %v, key %v", id, err, *keys)
	}
	job, err := c.Get(ctx, "j-1")
	if err != nil || !job.Status.Finished() || len(job.Results) != 1 || *job.Results[0].Stdout != "3" {
		t.Fatalf("get = %+v, %v", job, err)
	}
	if _, err := c.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}

	for lang, want := range map[string]error{"cobol": ErrRejected, "quota": ErrBusy, "boom": ErrUnavailable} {
		_, err := c.Execute(ctx, ExecuteRequest{Language: lang}, "")
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", lang, err, want)
		}
	}
	_, err = c.Execute(ctx, ExecuteRequest{Language: "cobol"}, "")
	if !strings.Contains(err.Error(), "unknown language: cobol") {
		t.Errorf("judge message not surfaced: %v", err)
	}

	ids, err := c.ExecuteBatch(ctx, []ExecuteRequest{{Language: "python"}, {Language: "go"}}, "b")
	if err != nil || len(ids) != 2 || ids[0] != "b-a" || ids[1] != "b-b" {
		t.Fatalf("batch = %v, %v", ids, err)
	}
	if _, err := c.ExecuteBatch(ctx, []ExecuteRequest{{Language: "python"}, {Language: "cobol"}}, "b2"); !errors.Is(err, ErrRejected) {
		t.Fatalf("partially refused batch must fail as a whole: %v", err)
	}

	if _, err := New(srv.URL, "wrong", nil).Execute(ctx, ExecuteRequest{Language: "python"}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("bad API key must be retryable (operator problem): %v", err)
	}
	if _, err := New("", "", nil).Execute(ctx, ExecuteRequest{}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured judge: %v", err)
	}
}

// Requests name an exact runtime, so the judge never picks a version on the CMS's behalf.
func TestClientPinsRuntimes(t *testing.T) {
	srv, _, languages := fakeServer(t)
	c := New(srv.URL, "secret", map[string]string{"python": "python-3.12", "go": "go-1.24"})
	ctx := context.Background()
	if _, err := c.Execute(ctx, ExecuteRequest{Language: "python"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteBatch(ctx, []ExecuteRequest{{Language: "go"}, {Language: "python"}}, "b"); err != nil {
		t.Fatal(err)
	}
	want := []string{"python-3.12", "go-1.24", "python-3.12"}
	if strings.Join(*languages, ",") != strings.Join(want, ",") {
		t.Fatalf("judge saw %v, want %v", *languages, want)
	}
}
