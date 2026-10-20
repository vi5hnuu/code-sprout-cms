// Package asset owns uploaded files: issuing presigned upload URLs, verifying what arrived,
// and describing files to the domains that reference them (content images, problem test
// sets, avatars).
//
// Flow:
//  1. CreateUpload validates purpose/type/size, records a PENDING file and returns a
//     presigned PUT that only accepts exactly those bytes.
//  2. The client uploads straight to storage.
//  3. Complete checks size and type, then proves the SHA-256: instantly when storage verified
//     it (S3, R2), by hashing inline for small files, or via the worker (VERIFYING) for large
//     ones. A mismatch quarantines the file and deletes the object.
package asset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // register decoders for DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset/assetdb"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/storage"
)

type Purpose string

const (
	PurposeAsset   Purpose = "ASSET"    // images/videos embedded in content (public)
	PurposeAvatar  Purpose = "AVATAR"   // profile pictures (public)
	PurposeTestSet Purpose = "TEST_SET" // hidden problem test cases (private)
	PurposeChecker Purpose = "CHECKER"  // custom checker source (private)
	PurposeArchive Purpose = "ARCHIVE"  // server-written cold storage (e.g. old revisions); never uploaded
)

type Status string

const (
	StatusPending     Status = "PENDING"
	StatusVerifying   Status = "VERIFYING"
	StatusReady       Status = "READY"
	StatusQuarantined Status = "QUARANTINED"
	// StatusDeleting: let go of by the system (an erased account's avatars); garbage collection
	// removes it. Never returned to clients: its owner no longer exists.
	StatusDeleting Status = "DELETING"
)

const (
	MiB = 1 << 20
	// Files up to this size are hashed during /complete when storage did not verify the
	// checksum itself; larger ones go to the worker so the request stays fast.
	defaultInlineVerifyLimit = 32 * MiB
	presignTTL               = 15 * time.Minute
	verifyLease              = 10 * time.Minute
	// Image headers (dimensions) sit at the start of the file; this is generous even for JPEGs
	// with large EXIF blocks. A file whose header lies beyond it is stored without dimensions.
	imageHeaderBytes = 512 << 10
)

type mimeRule struct {
	ext      string
	maxBytes int64
	image    bool
}

type purposeRule struct {
	public bool
	mimes  map[string]mimeRule
}

// SVG is deliberately absent: it can carry script, and an uploaded SVG opened directly from
// the asset domain would run it.
var rules = map[Purpose]purposeRule{
	PurposeAsset: {public: true, mimes: map[string]mimeRule{
		"image/png":  {".png", 10 * MiB, true},
		"image/jpeg": {".jpg", 10 * MiB, true},
		"image/webp": {".webp", 10 * MiB, true},
		"image/gif":  {".gif", 10 * MiB, true},
		"video/mp4":  {".mp4", 500 * MiB, false},
		"video/webm": {".webm", 500 * MiB, false},
	}},
	PurposeAvatar: {public: true, mimes: map[string]mimeRule{
		"image/png":  {".png", 2 * MiB, true},
		"image/jpeg": {".jpg", 2 * MiB, true},
		"image/webp": {".webp", 2 * MiB, true},
	}},
	PurposeTestSet: {public: false, mimes: map[string]mimeRule{
		"application/zip":      {".zip", 1024 * MiB, false},
		"application/x-ndjson": {".ndjson", 1024 * MiB, false},
	}},
	PurposeChecker: {public: false, mimes: map[string]mimeRule{
		"text/plain": {".txt", 1 * MiB, false},
	}},
}

type Config struct {
	PublicBucket  string
	PrivateBucket string
	PublicBaseURL string
}

type File struct {
	ID        int64   `json:"id"`
	Purpose   Purpose `json:"purpose"`
	Mime      string  `json:"mime"`
	SizeBytes int64   `json:"sizeBytes"`
	Status    Status  `json:"status"`
	Width     *int32  `json:"width"`
	Height    *int32  `json:"height"`
	// URL is set for public files once READY. Private files are never addressable by URL.
	URL *string `json:"url"`
}

type CreateUpload struct {
	Purpose   Purpose `json:"purpose"`
	Mime      string  `json:"mime"`
	SizeBytes int64   `json:"sizeBytes"`
	SHA256    string  `json:"sha256"` // hex
}

type UploadTicket struct {
	File File `json:"file"`
	// Upload is nil when an identical READY file already exists (AlreadyUploaded).
	Upload          *storage.PresignedUpload `json:"upload"`
	AlreadyUploaded bool                     `json:"alreadyUploaded"`
}

type Service struct {
	db                *db.DB
	store             storage.Store
	cfg               Config
	inlineVerifyLimit int64
}

func NewService(d *db.DB, store storage.Store, cfg Config) *Service {
	return &Service{db: d, store: store, cfg: cfg, inlineVerifyLimit: defaultInlineVerifyLimit}
}

func (s *Service) view(f assetdb.File) File {
	v := File{
		ID: f.ID, Purpose: Purpose(f.Purpose), Mime: f.Mime, SizeBytes: f.SizeBytes,
		Status: Status(f.Status), Width: f.Width, Height: f.Height,
	}
	if v.Status == StatusReady && f.Bucket == s.cfg.PublicBucket {
		u := s.cfg.PublicBaseURL + "/" + f.StorageKey
		v.URL = &u
	}
	return v
}

func (s *Service) CreateUpload(ctx context.Context, p auth.Principal, in CreateUpload) (UploadTicket, error) {
	if err := access.RequireAuthor(p); err != nil {
		return UploadTicket{}, err
	}
	rule, mr, sum, err := validateUpload(in)
	if err != nil {
		return UploadTicket{}, err
	}
	q := assetdb.New(s.db.Primary)

	dup, err := q.FindReadyDuplicate(ctx, assetdb.FindReadyDuplicateParams{OwnerID: &p.UserID, Purpose: string(in.Purpose), Sha256: sum})
	if err == nil {
		return UploadTicket{File: s.view(dup), AlreadyUploaded: true}, nil
	}
	if !db.IsNoRows(err) {
		return UploadTicket{}, err
	}

	bucket := s.cfg.PrivateBucket
	if rule.public {
		bucket = s.cfg.PublicBucket
	}
	key, err := newStorageKey(in.Purpose, mr.ext)
	if err != nil {
		return UploadTicket{}, err
	}
	f, err := q.InsertFile(ctx, assetdb.InsertFileParams{
		OwnerID: &p.UserID, Bucket: bucket, StorageKey: key, Mime: in.Mime,
		SizeBytes: in.SizeBytes, Sha256: sum, Purpose: string(in.Purpose),
	})
	if err != nil {
		return UploadTicket{}, err
	}
	up, err := s.store.PresignPut(ctx, bucket, key, in.Mime, in.SizeBytes, sum, presignTTL)
	if err != nil {
		return UploadTicket{}, err
	}
	return UploadTicket{File: s.view(f), Upload: &up}, nil
}

func validateUpload(in CreateUpload) (purposeRule, mimeRule, []byte, error) {
	fields := map[string]string{}
	rule, ok := rules[in.Purpose]
	if !ok {
		fields["purpose"] = "purpose must be ASSET, AVATAR, TEST_SET or CHECKER."
	}
	mr, mimeOK := rule.mimes[in.Mime]
	if ok && !mimeOK {
		allowed := make([]string, 0, len(rule.mimes))
		for m := range rule.mimes {
			allowed = append(allowed, m)
		}
		slices.Sort(allowed)
		fields["mime"] = fmt.Sprintf("Unsupported type for %s; allowed: %s.", in.Purpose, strings.Join(allowed, ", "))
	}
	if in.SizeBytes <= 0 {
		fields["sizeBytes"] = "sizeBytes must be positive."
	} else if mimeOK && in.SizeBytes > mr.maxBytes {
		fields["sizeBytes"] = fmt.Sprintf("Files of this type may be at most %d MiB.", mr.maxBytes/MiB)
	}
	sum, err := hex.DecodeString(in.SHA256)
	if err != nil || len(sum) != sha256.Size {
		fields["sha256"] = "sha256 must be the file's SHA-256 as 64 hex characters."
	}
	if len(fields) > 0 {
		return purposeRule{}, mimeRule{}, nil, apperr.ValidationFailed(fields)
	}
	return rule, mr, sum, nil
}

// newStorageKey builds `<purpose>/<yyyy>/<mm>/<random><ext>`. Random names are unguessable
// (private objects can't be probed), never collide, and the date prefix keeps listings and
// lifecycle rules manageable at millions of objects.
func newStorageKey(p Purpose, ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s%s", strings.ToLower(string(p)), time.Now().UTC().Format("2006/01"), hex.EncodeToString(b[:]), ext), nil
}

func (s *Service) Get(ctx context.Context, p auth.Principal, id int64) (File, error) {
	f, err := s.ownedFile(ctx, p, id)
	if err != nil {
		return File{}, err
	}
	return s.view(f), nil
}

func (s *Service) ownedFile(ctx context.Context, p auth.Principal, id int64) (assetdb.File, error) {
	f, err := assetdb.New(s.db.Primary).GetFile(ctx, id)
	if db.IsNoRows(err) || (err == nil && !p.IsAdmin && (f.OwnerID == nil || *f.OwnerID != p.UserID)) {
		// Someone else's upload looks exactly like a missing one.
		return assetdb.File{}, apperr.NotFound("upload.not_found", "No such upload.")
	}
	return f, err
}

// Complete verifies an uploaded file. It is idempotent: completing a READY or VERIFYING file
// just returns its current state.
func (s *Service) Complete(ctx context.Context, p auth.Principal, id int64) (File, error) {
	f, err := s.ownedFile(ctx, p, id)
	if err != nil {
		return File{}, err
	}
	switch Status(f.Status) {
	case StatusReady, StatusVerifying:
		return s.view(f), nil
	case StatusQuarantined:
		return File{}, apperr.Conflict("upload.quarantined", "This upload failed verification; upload the file again.")
	}

	info, err := s.store.Head(ctx, f.Bucket, f.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return File{}, apperr.Conflict("upload.missing", "The file has not been uploaded yet.")
	}
	if err != nil {
		return File{}, err
	}
	if info.Size != f.SizeBytes || !strings.EqualFold(info.ContentType, f.Mime) {
		return File{}, s.quarantine(ctx, f, fmt.Sprintf("stored %d bytes of %q, expected %d of %q", info.Size, info.ContentType, f.SizeBytes, f.Mime))
	}

	switch {
	case info.SHA256 != nil:
		if subtle.ConstantTimeCompare(info.SHA256, f.Sha256) != 1 {
			return File{}, s.quarantine(ctx, f, "storage checksum differs from declared sha256")
		}
	case f.SizeBytes <= s.inlineVerifyLimit:
		if err := s.verifyHash(ctx, f); err != nil {
			return File{}, err
		}
	default:
		v, err := assetdb.New(s.db.Primary).MarkFileVerifying(ctx, f.ID)
		if err != nil {
			return File{}, err
		}
		return s.view(v), nil
	}
	return s.markReady(ctx, f)
}

// VerifyNext hashes one large file awaiting verification. It reports whether there was work,
// so the worker can loop until the queue is empty.
func (s *Service) VerifyNext(ctx context.Context) (bool, error) {
	q := assetdb.New(s.db.Primary)
	f, err := q.LeaseVerifyingFile(ctx, int32(verifyLease.Seconds()))
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.verifyHash(ctx, f)
	if err == nil {
		_, err = s.markReady(ctx, f)
	}
	if _, isDomain := apperr.As(err); isDomain {
		return true, nil // quarantined; nothing to retry
	}
	return true, err // nil, or a storage hiccup: the lease expires and another pass retries
}

// verifyHash streams the object and compares its SHA-256, quarantining on mismatch.
func (s *Service) verifyHash(ctx context.Context, f assetdb.File) error {
	r, err := s.store.Open(ctx, f.Bucket, f.StorageKey, 0)
	if err != nil {
		return err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("hash %d: %w", f.ID, err)
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), f.Sha256) != 1 {
		return s.quarantine(ctx, f, "content does not match declared sha256")
	}
	return nil
}

func (s *Service) markReady(ctx context.Context, f assetdb.File) (File, error) {
	var width, height *int32
	if rules[Purpose(f.Purpose)].mimes[f.Mime].image {
		w, h, err := s.imageSize(ctx, f)
		if err != nil {
			return File{}, err
		}
		width, height = &w, &h
	}
	ready, err := assetdb.New(s.db.Primary).MarkFileReady(ctx, assetdb.MarkFileReadyParams{ID: f.ID, Width: width, Height: height})
	if err != nil {
		return File{}, err
	}
	return s.view(ready), nil
}

// imageSize decodes only the image header. A file declared as an image must really be one, in
// the declared format: anything else (a polyglot, an HTML page renamed .png) is quarantined
// rather than served from the public bucket under an image type. A storage error is returned
// as is, so the caller can retry.
func (s *Service) imageSize(ctx context.Context, f assetdb.File) (int32, int32, error) {
	r, err := s.store.Open(ctx, f.Bucket, f.StorageKey, imageHeaderBytes)
	if err != nil {
		return 0, 0, fmt.Errorf("image header %d: %w", f.ID, err)
	}
	defer r.Close()
	cfg, format, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, s.quarantine(ctx, f, "not a decodable image: "+err.Error())
	}
	if "image/"+format != f.Mime || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, s.quarantine(ctx, f, fmt.Sprintf("image is %s %dx%d, declared %s", format, cfg.Width, cfg.Height, f.Mime))
	}
	return int32(cfg.Width), int32(cfg.Height), nil
}

// quarantine marks a file failed and deletes its object, returning the error to report.
func (s *Service) quarantine(ctx context.Context, f assetdb.File, reason string) error {
	slog.Warn("upload quarantined", "file", f.ID, "reason", reason)
	if _, err := assetdb.New(s.db.Primary).MarkFileQuarantined(ctx, f.ID); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, f.Bucket, f.StorageKey); err != nil {
		slog.Warn("delete quarantined object", "file", f.ID, "err", err) // GC retries later
	}
	return apperr.Conflict("upload.mismatch", "The uploaded file does not match what was declared; upload it again.")
}

// Describe returns READY files by id, for domains that embed them (content publish resolves
// asset:<id> references through it). Missing or not-ready ids are simply absent.
func (s *Service) Describe(ctx context.Context, ids []int64) (map[int64]File, error) {
	out := make(map[int64]File, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := assetdb.New(s.db.Primary).ListFilesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range rows {
		if Status(f.Status) == StatusReady {
			out[f.ID] = s.view(f)
		}
	}
	return out, nil
}

// ConfigFrom selects the asset settings from process configuration.
func ConfigFrom(c config.StorageConfig) Config {
	return Config{PublicBucket: c.PublicBucket, PrivateBucket: c.PrivateBucket, PublicBaseURL: c.PublicBaseURL}
}

// Stored is a READY file with the details other domains need to trust it: who uploaded it and
// its verified checksum. It is for server-side use and never serialised to clients.
type Stored struct {
	File
	OwnerID *int64
	SHA256  []byte
}

var errNotReady = apperr.NotFound("upload.not_found", "No such verified upload.")

// Stored returns a READY file's server-side details.
func (s *Service) Stored(ctx context.Context, id int64) (Stored, error) {
	f, err := assetdb.New(s.db.Primary).GetFile(ctx, id)
	if db.IsNoRows(err) || (err == nil && Status(f.Status) != StatusReady) {
		return Stored{}, errNotReady
	}
	if err != nil {
		return Stored{}, err
	}
	return Stored{File: s.view(f), OwnerID: f.OwnerID, SHA256: f.Sha256}, nil
}

// Open streams a READY file, public or private, for server-side processing (e.g. parsing a
// test set). The caller must close the reader.
func (s *Service) Open(ctx context.Context, id int64) (io.ReadCloser, error) {
	f, err := assetdb.New(s.db.Primary).GetFile(ctx, id)
	if db.IsNoRows(err) || (err == nil && Status(f.Status) != StatusReady) {
		return nil, errNotReady
	}
	if err != nil {
		return nil, err
	}
	return s.store.Open(ctx, f.Bucket, f.StorageKey, 0)
}

// StoreInternal saves server-generated bytes (e.g. an archived revision) to the private bucket
// and registers them as a READY file. Returns the file id.
func (s *Service) StoreInternal(ctx context.Context, purpose Purpose, mime, ext string, body []byte) (int64, error) {
	key, err := newStorageKey(purpose, ext)
	if err != nil {
		return 0, err
	}
	if err := s.store.Put(ctx, s.cfg.PrivateBucket, key, mime, body); err != nil {
		return 0, err
	}
	sum := sha256.Sum256(body)
	return assetdb.New(s.db.Primary).InsertReadyFile(ctx, assetdb.InsertReadyFileParams{
		Bucket: s.cfg.PrivateBucket, StorageKey: key, Mime: mime, SizeBytes: int64(len(body)), Sha256: sum[:], Purpose: string(purpose),
	})
}

// Garbage collection windows: an upload URL lives 15 minutes, so a day-old PENDING row is an
// abandoned upload; quarantined rows are kept a week for investigation.
const (
	pendingTTL     = 24 * time.Hour
	quarantinedTTL = 7 * 24 * time.Hour
	gcBatch        = 500
)

// CollectGarbage deletes abandoned and quarantined uploads (objects and rows). READY files are
// never collected here: drafts may reference them, and storage is cheaper than a wrong delete.
func (s *Service) CollectGarbage(ctx context.Context) (int, error) {
	removed := 0
	q := assetdb.New(s.db.Primary)
	pending, err := q.ListStalePendingFiles(ctx, assetdb.ListStalePendingFilesParams{Before: time.Now().Add(-pendingTTL), RowLimit: gcBatch})
	if err != nil {
		return removed, err
	}
	quarantined, err := q.ListStaleQuarantinedFiles(ctx, assetdb.ListStaleQuarantinedFilesParams{Before: time.Now().Add(-quarantinedTTL), RowLimit: gcBatch})
	if err != nil {
		return removed, err
	}
	deleting, err := q.ListDeletingFiles(ctx, gcBatch)
	if err != nil {
		return removed, err
	}
	for _, files := range [][]assetdb.File{pending, quarantined, deleting} {
		for _, f := range files {
			// The row goes first: one still referenced cannot be deleted, and its object must
			// then stay too. An object left behind by a failed delete only costs storage.
			if err := q.DeleteFile(ctx, f.ID); err != nil {
				slog.Warn("gc: delete row", "file", f.ID, "err", err) // still referenced: keep it
				continue
			}
			if err := s.store.Delete(ctx, f.Bucket, f.StorageKey); err != nil {
				slog.Warn("gc: delete object; it is now orphaned", "file", f.ID, "bucket", f.Bucket, "key", f.StorageKey, "err", err)
			}
			removed++
		}
	}
	return removed, nil
}
