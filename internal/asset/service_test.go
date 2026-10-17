package asset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/storage"
	"code-sprout-cms/internal/platform/testenv"
)

const testBaseURL = "http://localhost:9010/sprout-public"

func newTestService(t *testing.T) (*Service, *storage.S3) {
	t.Helper()
	d := testenv.DB(t)
	store := storage.NewS3(storage.Config{
		Endpoint: testenv.StorageEndpoint(t), Region: "us-east-1", AccessKey: "sprout", SecretKey: "sprout-secret",
	})
	return NewService(d, store, Config{PublicBucket: "sprout-public", PrivateBucket: "sprout-private", PublicBaseURL: testBaseURL}), store
}

// pngBytes returns a unique w×h PNG (the seed varies a pixel so each test uploads new bytes).
func pngBytes(t *testing.T, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: seed, G: byte(len(t.Name())), B: 7, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// put performs the client side of the upload exactly as a browser would.
func put(t *testing.T, ticket UploadTicket, body []byte) {
	t.Helper()
	req, err := http.NewRequest(ticket.Upload.Method, ticket.Upload.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range ticket.Upload.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload PUT = %d", resp.StatusCode)
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if e, ok := apperr.As(err); !ok || e.Code != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
}

func TestImageUploadLifecycle(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	author := testenv.User(t, svc.db)
	body := pngBytes(t, 3, 2, 1)

	ticket, err := svc.CreateUpload(ctx, author, CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: int64(len(body)), SHA256: hexSHA(body)})
	if err != nil {
		t.Fatal(err)
	}
	if ticket.AlreadyUploaded || ticket.Upload == nil || ticket.File.Status != StatusPending || ticket.File.URL != nil {
		t.Fatalf("ticket = %+v", ticket)
	}

	_, err = svc.Complete(ctx, author, ticket.File.ID)
	wantCode(t, err, "upload.missing")

	put(t, ticket, body)
	f, err := svc.Complete(ctx, author, ticket.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != StatusReady || f.URL == nil || f.Width == nil || *f.Width != 3 || *f.Height != 2 {
		t.Fatalf("completed file = %+v", f)
	}
	again, err := svc.Complete(ctx, author, ticket.File.ID) // idempotent
	if err != nil || again.Status != StatusReady {
		t.Fatalf("second complete = %+v, %v", again, err)
	}

	resp, err := http.Get(*f.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public URL = %d", resp.StatusCode)
	}

	// The same bytes again reuse the READY file without a new upload.
	dup, err := svc.CreateUpload(ctx, author, CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: int64(len(body)), SHA256: hexSHA(body)})
	if err != nil || !dup.AlreadyUploaded || dup.File.ID != f.ID || dup.Upload != nil {
		t.Fatalf("dedup = %+v, %v", dup, err)
	}

	// Another user can neither see nor complete it.
	other := testenv.User(t, svc.db)
	_, err = svc.Get(ctx, other, f.ID)
	wantCode(t, err, "upload.not_found")

	described, err := svc.Describe(ctx, []int64{f.ID, 999999999})
	if err != nil || len(described) != 1 || described[f.ID].URL == nil {
		t.Fatalf("describe = %+v, %v", described, err)
	}
}

func TestTamperedUploadIsQuarantined(t *testing.T) {
	svc, store := newTestService(t)
	ctx := context.Background()
	author := testenv.User(t, svc.db)
	declared := pngBytes(t, 4, 4, 2)
	tampered := bytes.Clone(declared)
	tampered[len(tampered)-5] ^= 0xFF // same length, different bytes

	ticket, err := svc.CreateUpload(ctx, author, CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: int64(len(declared)), SHA256: hexSHA(declared)})
	if err != nil {
		t.Fatal(err)
	}
	// Storage that verifies checksums (S3, R2) rejects this PUT outright; SeaweedFS stores it,
	// so the API's own verification must catch it.
	req, _ := http.NewRequest(ticket.Upload.Method, ticket.Upload.URL, bytes.NewReader(tampered))
	for k, v := range ticket.Upload.Headers {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	_, err = svc.Complete(ctx, author, ticket.File.ID)
	wantCode(t, err, "upload.mismatch")
	got, _ := svc.Get(ctx, author, ticket.File.ID)
	if got.Status != StatusQuarantined {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := store.Head(ctx, "sprout-public", keyOf(t, svc, ticket.File.ID)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("quarantined object still stored (err=%v)", err)
	}
	_, err = svc.Complete(ctx, author, ticket.File.ID)
	wantCode(t, err, "upload.quarantined")
}

func TestLargeFileVerifiedByWorker(t *testing.T) {
	svc, _ := newTestService(t)
	svc.inlineVerifyLimit = 0 // treat every file as large
	ctx := context.Background()
	author := testenv.User(t, svc.db)
	body := []byte(`{"input":"1 2","output":"3"}` + "\n" + `{"input":"` + testenv.Unique("x") + `","output":"1"}` + "\n")

	ticket, err := svc.CreateUpload(ctx, author, CreateUpload{Purpose: PurposeTestSet, Mime: "application/x-ndjson", SizeBytes: int64(len(body)), SHA256: hexSHA(body)})
	if err != nil {
		t.Fatal(err)
	}
	put(t, ticket, body)
	f, err := svc.Complete(ctx, author, ticket.File.ID)
	if err != nil || f.Status != StatusVerifying {
		t.Fatalf("complete = %+v, %v", f, err)
	}
	if f.URL != nil {
		t.Fatal("private files must never get a URL")
	}

	// Drain the queue (other tests' leftovers may be in it too).
	for {
		worked, err := svc.VerifyNext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	f, _ = svc.Get(ctx, author, ticket.File.ID)
	if f.Status != StatusReady || f.URL != nil {
		t.Fatalf("after worker = %+v", f)
	}
}

func TestUploadValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	author := testenv.User(t, svc.db)
	sha := hexSHA([]byte("x"))

	cases := []struct {
		name  string
		in    CreateUpload
		field string
	}{
		{"svg refused", CreateUpload{Purpose: PurposeAsset, Mime: "image/svg+xml", SizeBytes: 10, SHA256: sha}, "mime"},
		{"oversize image", CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: 11 * MiB, SHA256: sha}, "sizeBytes"},
		{"zero size", CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: 0, SHA256: sha}, "sizeBytes"},
		{"bad sha", CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: 10, SHA256: "abc"}, "sha256"},
		{"archive not uploadable", CreateUpload{Purpose: "ARCHIVE", Mime: "application/zip", SizeBytes: 10, SHA256: sha}, "purpose"},
		{"test set not an image", CreateUpload{Purpose: PurposeTestSet, Mime: "image/png", SizeBytes: 10, SHA256: sha}, "mime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateUpload(ctx, author, tc.in)
			e, ok := apperr.As(err)
			if !ok || e.Fields[tc.field] == "" {
				t.Fatalf("err = %v, want a %s field error", err, tc.field)
			}
		})
	}

	guest := auth.Principal{UserID: author.UserID, IsGuest: true}
	_, err := svc.CreateUpload(ctx, guest, CreateUpload{Purpose: PurposeAsset, Mime: "image/png", SizeBytes: 10, SHA256: sha})
	wantCode(t, err, "access.guest")
}

func keyOf(t *testing.T, svc *Service, id int64) string {
	t.Helper()
	var key string
	if err := svc.db.Primary.QueryRow(context.Background(), "SELECT storage_key FROM file WHERE id=$1", id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}
