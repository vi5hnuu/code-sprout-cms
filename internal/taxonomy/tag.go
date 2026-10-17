package taxonomy

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/taxonomy/taxonomydb"
)

type TagKind string

const (
	TagTopic    TagKind = "TOPIC"
	TagCompany  TagKind = "COMPANY"
	TagLanguage TagKind = "LANGUAGE"
	TagPattern  TagKind = "PATTERN"
)

func (k TagKind) valid() bool {
	switch k {
	case TagTopic, TagCompany, TagLanguage, TagPattern:
		return true
	}
	return false
}

type Tag struct {
	ID          int64   `json:"id"`
	Kind        TagKind `json:"kind"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type CreateTag struct {
	Kind        TagKind `json:"kind"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type UpdateTag struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// TagCursor resumes an alphabetical listing.
type TagCursor struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

const maxTagSearch = 20

func tagFromRow(t taxonomydb.Tag) Tag {
	return Tag{ID: t.ID, Kind: TagKind(t.Kind), Slug: t.Slug, Name: t.Name, Description: t.Description}
}

func invalidKind() error {
	return apperr.ValidationFailed(map[string]string{"kind": "kind must be TOPIC, COMPANY, LANGUAGE or PATTERN."})
}

func (s *Service) ListTags(ctx context.Context, kind TagKind, req page.Request[TagCursor]) (page.Page[Tag], error) {
	if !kind.valid() {
		return page.Page[Tag]{}, invalidKind()
	}
	var afterName *string
	var afterID *int64
	if req.Key != nil {
		afterName, afterID = &req.Key.Name, &req.Key.ID
	}
	rows, err := taxonomydb.New(s.db.Replica).ListTags(ctx, taxonomydb.ListTagsParams{
		Kind: string(kind), AfterName: afterName, AfterID: afterID, RowLimit: int32(req.Limit + 1),
	})
	if err != nil {
		return page.Page[Tag]{}, err
	}
	tags := make([]Tag, len(rows))
	for i, r := range rows {
		tags[i] = tagFromRow(r)
	}
	return page.Build(tags, req.Limit, func(t Tag) TagCursor { return TagCursor{Name: t.Name, ID: t.ID} }), nil
}

// SearchTags powers the studio tag picker; kind is optional.
func (s *Service) SearchTags(ctx context.Context, q string, kind *TagKind) ([]Tag, error) {
	q = strings.TrimSpace(q)
	if len(q) < 2 {
		return []Tag{}, nil
	}
	var k *string
	if kind != nil {
		if !kind.valid() {
			return nil, invalidKind()
		}
		ks := string(*kind)
		k = &ks
	}
	rows, err := taxonomydb.New(s.db.Replica).SearchTags(ctx, taxonomydb.SearchTagsParams{Q: q, Kind: k, RowLimit: maxTagSearch})
	if err != nil {
		return nil, err
	}
	tags := make([]Tag, len(rows))
	for i, r := range rows {
		tags[i] = tagFromRow(r)
	}
	return tags, nil
}

func (s *Service) TagBySlug(ctx context.Context, kind TagKind, slug string) (Tag, error) {
	t, err := taxonomydb.New(s.db.Replica).GetTagBySlug(ctx, taxonomydb.GetTagBySlugParams{Kind: string(kind), Slug: slug})
	if db.IsNoRows(err) {
		return Tag{}, apperr.NotFound("tag.not_found", "No such tag.")
	}
	if err != nil {
		return Tag{}, err
	}
	return tagFromRow(t), nil
}

func (s *Service) CreateTag(ctx context.Context, p auth.Principal, in CreateTag) (Tag, error) {
	fields := map[string]string{}
	validateName(fields, in.Name)
	validateSlug(fields, in.Slug)
	if !in.Kind.valid() {
		fields["kind"] = "kind must be TOPIC, COMPANY, LANGUAGE or PATTERN."
	}
	if len(fields) > 0 {
		return Tag{}, apperr.ValidationFailed(fields)
	}
	if err := s.policy.Require(ctx, p, access.RoleEditor, nil); err != nil {
		return Tag{}, err
	}
	var tag Tag
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		id, err := q.InsertTag(ctx, taxonomydb.InsertTagParams{
			Kind: string(in.Kind), Slug: in.Slug, Name: strings.TrimSpace(in.Name), Description: in.Description,
		})
		if c, ok := db.ConstraintViolation(err); ok && c == "tag_kind_slug_key" {
			return apperr.Conflict("tag.slug_taken", "A tag with this slug already exists.")
		}
		if err != nil {
			return err
		}
		tag = Tag{ID: id, Kind: in.Kind, Slug: in.Slug, Name: strings.TrimSpace(in.Name), Description: in.Description}
		return s.recordTagChange(ctx, tx, p, "tag.create", tag)
	})
	return tag, err
}

func (s *Service) UpdateTag(ctx context.Context, p auth.Principal, id int64, in UpdateTag) (Tag, error) {
	fields := map[string]string{}
	validateName(fields, in.Name)
	if len(fields) > 0 {
		return Tag{}, apperr.ValidationFailed(fields)
	}
	if err := s.policy.Require(ctx, p, access.RoleEditor, nil); err != nil {
		return Tag{}, err
	}
	var tag Tag
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		row, err := q.GetTag(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("tag.not_found", "No such tag.")
		}
		if err != nil {
			return err
		}
		if err := q.UpdateTag(ctx, taxonomydb.UpdateTagParams{ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description}); err != nil {
			return err
		}
		tag = tagFromRow(row)
		tag.Name, tag.Description = strings.TrimSpace(in.Name), in.Description
		return s.recordTagChange(ctx, tx, p, "tag.update", tag)
	})
	return tag, err
}

// DeleteTag detaches the tag from every item (content_tag cascades). Admin-only because it
// can touch thousands of pages at once.
func (s *Service) DeleteTag(ctx context.Context, p auth.Principal, id int64) error {
	if err := s.policy.Require(ctx, p, access.RoleAdmin, nil); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		row, err := q.GetTag(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("tag.not_found", "No such tag.")
		}
		if err != nil {
			return err
		}
		if err := q.DeleteTag(ctx, id); err != nil {
			return err
		}
		return s.recordTagChange(ctx, tx, p, "tag.delete", tagFromRow(row))
	})
}

func (s *Service) recordTagChange(ctx context.Context, tx pgx.Tx, p auth.Principal, action string, t Tag) error {
	if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, Entity: "tag", EntityID: t.ID, Data: t}); err != nil {
		return err
	}
	return outbox.Publish(ctx, tx, outbox.TopicTagChanged, t.ID, outbox.TagEvent{Kind: string(t.Kind), Slug: t.Slug})
}

// ExistingTagIDs returns which of ids are real tags (sorted), for domains attaching tags.
func (s *Service) ExistingTagIDs(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	return taxonomydb.New(s.db.Primary).ExistingTagIDs(ctx, ids)
}

// TagsByID batch-loads tags for display, ordered by kind then name.
func (s *Service) TagsByID(ctx context.Context, ids []int64) ([]Tag, error) {
	if len(ids) == 0 {
		return []Tag{}, nil
	}
	rows, err := taxonomydb.New(s.db.Replica).ListTagsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	tags := make([]Tag, len(rows))
	for i, r := range rows {
		tags[i] = tagFromRow(r)
	}
	return tags, nil
}
