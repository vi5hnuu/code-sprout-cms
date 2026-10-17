package content

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/content/contentdb"
)

// Old revision bodies move to cold object storage (gzip) so the hot revision table stays
// bounded however long an item's history grows. The draft, the live revision and the latest
// archiveKeep revisions always stay in Postgres; archived ones still load, just slower.
const (
	archiveKeep  = 20
	archiveAfter = 30 * 24 * time.Hour
	archiveBatch = 200
)

// Archive is what archiving needs from file storage (implemented by asset.Service).
type Archive interface {
	StoreInternal(ctx context.Context, purpose asset.Purpose, mime, ext string, body []byte) (int64, error)
	Open(ctx context.Context, id int64) (io.ReadCloser, error)
}

// ArchiveRevisions moves one batch of eligible bodies to storage, reporting how many moved.
// The row is cleared only after its archive is safely stored, so a crash loses nothing (at
// worst an orphan archive object).
func (s *Service) ArchiveRevisions(ctx context.Context, store Archive) (int, error) {
	q := contentdb.New(s.db.Primary)
	rows, err := q.ListArchivableRevisions(ctx, contentdb.ListArchivableRevisionsParams{
		Before: time.Now().Add(-archiveAfter), Keep: archiveKeep, RowLimit: archiveBatch,
	})
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, r := range rows {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write([]byte(*r.BodyMd)); err != nil {
			return moved, err
		}
		if err := zw.Close(); err != nil {
			return moved, err
		}
		fileID, err := store.StoreInternal(ctx, asset.PurposeArchive, "application/gzip", ".md.gz", buf.Bytes())
		if err != nil {
			return moved, fmt.Errorf("archive %d/%d: %w", r.ContentID, r.RevNo, err)
		}
		if err := q.MarkRevisionArchived(ctx, contentdb.MarkRevisionArchivedParams{ContentID: r.ContentID, RevNo: r.RevNo, FileID: &fileID}); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// readArchived loads an archived body back from storage.
func readArchived(ctx context.Context, store Archive, fileID int64) (string, error) {
	rc, err := store.Open(ctx, fileID)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	zr, err := gzip.NewReader(rc)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	body, err := io.ReadAll(io.LimitReader(zr, 2<<20))
	return string(body), err
}
