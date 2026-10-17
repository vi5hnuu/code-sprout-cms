// Package taxonomy owns how content is organised: the category tree (sections such as
// DSA › Algorithms › Searching) and flat tags (topics, companies, languages, patterns).
package taxonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/taxonomy/taxonomydb"
)

// MaxDepth bounds the tree. Deeper hierarchies hurt navigation and breadcrumbs more than they
// help organisation; tags handle the long tail.
const MaxDepth = 6

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// reservedRootSlugs are site routes. Articles live at /{rootCategorySlug}/{slug}, so a root
// category named like one of these would collide with that page (see content.PublicPath).
var reservedRootSlugs = map[string]bool{
	"api": true, "articles": true, "auth": true, "blog": true, "category": true, "courses": true,
	"library": true, "lists": true, "problem-of-the-day": true, "problems": true, "quiz": true,
	"search": true, "series": true, "sitemaps": true, "studio": true, "study-plans": true,
	"tag": true, "topics": true, "u": true,
}

// Category is the API view. Path is the URL form ("dsa/algorithms"); the database stores the
// ltree form ("dsa.algorithms").
type Category struct {
	ID          int64           `json:"id"`
	ParentID    *int64          `json:"parentId"`
	Path        string          `json:"path"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Position    float64         `json:"position"`
	SEO         json.RawMessage `json:"seo"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// CategoryView is a node with its breadcrumb trail and direct children: everything a section
// page needs in one response.
type CategoryView struct {
	Category
	Ancestors []Category `json:"ancestors"`
	Children  []Category `json:"children"`
}

type CreateCategory struct {
	ParentID    *int64   `json:"parentId"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Position    *float64 `json:"position"` // omitted = append after the last sibling
}

type UpdateCategory struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	SEO         json.RawMessage `json:"seo"`
	Position    float64         `json:"position"`
}

// MoveCategory re-parents and/or renames a node; its whole subtree follows.
type MoveCategory struct {
	ParentID *int64 `json:"parentId"` // nil = make it a root
	Slug     string `json:"slug"`
}

type Service struct {
	db     *db.DB
	policy *access.Policy
}

func NewService(d *db.DB, policy *access.Policy) *Service {
	return &Service{db: d, policy: policy}
}

func toURLPath(ltreePath string) string { return strings.ReplaceAll(ltreePath, ".", "/") }

func toLtree(urlPath string) (string, bool) {
	parts := strings.Split(strings.Trim(urlPath, "/"), "/")
	if len(parts) > MaxDepth {
		return "", false
	}
	for _, p := range parts {
		if !slugPattern.MatchString(p) {
			return "", false
		}
	}
	return strings.Join(parts, "."), true
}

func fromRow(c taxonomydb.Category) Category {
	seo := json.RawMessage(c.Seo)
	if len(seo) == 0 {
		seo = json.RawMessage("{}")
	}
	return Category{
		ID: c.ID, ParentID: c.ParentID, Path: toURLPath(c.Path), Slug: c.Slug, Name: c.Name,
		Description: c.Description, Position: c.Position, SEO: seo, UpdatedAt: c.UpdatedAt,
	}
}

func fromRows(rows []taxonomydb.Category) []Category {
	out := make([]Category, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out
}

func (s *Service) Roots(ctx context.Context) ([]Category, error) {
	rows, err := taxonomydb.New(s.db.Replica).ListRootCategories(ctx)
	if err != nil {
		return nil, err
	}
	return fromRows(rows), nil
}

func (s *Service) ByPath(ctx context.Context, urlPath string) (CategoryView, error) {
	notFound := apperr.NotFound("category.not_found", "No such category.")
	path, ok := toLtree(urlPath)
	if !ok {
		return CategoryView{}, notFound
	}
	q := taxonomydb.New(s.db.Replica)
	c, err := q.GetCategoryByPath(ctx, path)
	if db.IsNoRows(err) {
		return CategoryView{}, notFound
	}
	if err != nil {
		return CategoryView{}, err
	}
	ancestors, err := q.ListCategoryAncestors(ctx, path)
	if err != nil {
		return CategoryView{}, err
	}
	children, err := q.ListChildCategories(ctx, &c.ID)
	if err != nil {
		return CategoryView{}, err
	}
	return CategoryView{Category: fromRow(c), Ancestors: fromRows(ancestors), Children: fromRows(children)}, nil
}

// Get reads from the primary: used right after writes and by other domains validating ids.
func (s *Service) Get(ctx context.Context, id int64) (Category, error) {
	c, err := taxonomydb.New(s.db.Primary).GetCategory(ctx, id)
	if db.IsNoRows(err) {
		return Category{}, apperr.NotFound("category.not_found", "No such category.")
	}
	if err != nil {
		return Category{}, err
	}
	return fromRow(c), nil
}

func validateName(fields map[string]string, name string) {
	if n := len(strings.TrimSpace(name)); n == 0 || n > 120 {
		fields["name"] = "Name is required and at most 120 characters."
	}
}

func validateSlug(fields map[string]string, slug string) {
	if len(slug) > 80 || !slugPattern.MatchString(slug) {
		fields["slug"] = "Use lower-case letters, digits and single hyphens (max 80)."
	}
}

func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateCategory) (Category, error) {
	fields := map[string]string{}
	validateName(fields, in.Name)
	validateSlug(fields, in.Slug)
	if len(fields) > 0 {
		return Category{}, apperr.ValidationFailed(fields)
	}
	// Top-level sections shape the whole site, so only admins add them.
	if in.ParentID == nil {
		if reservedRootSlugs[in.Slug] {
			return Category{}, apperr.ValidationFailed(map[string]string{"slug": "This slug is reserved for a site page."})
		}
		if err := s.policy.Require(ctx, p, access.RoleAdmin, nil); err != nil {
			return Category{}, err
		}
	} else if err := s.policy.Require(ctx, p, access.RoleEditor, in.ParentID); err != nil {
		return Category{}, err
	}

	var id int64
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		path := in.Slug
		if in.ParentID != nil {
			parent, err := q.GetCategory(ctx, *in.ParentID)
			if db.IsNoRows(err) {
				return apperr.ValidationFailed(map[string]string{"parentId": "No such category."})
			}
			if err != nil {
				return err
			}
			if strings.Count(parent.Path, ".")+2 > MaxDepth {
				return apperr.Invalid("category.too_deep", fmt.Sprintf("Categories nest at most %d levels.", MaxDepth))
			}
			path = parent.Path + "." + in.Slug
		}
		position, err := s.positionOrNext(ctx, q, in.ParentID, in.Position)
		if err != nil {
			return err
		}
		id, err = q.InsertCategory(ctx, taxonomydb.InsertCategoryParams{
			ParentID: in.ParentID, Path: path, Slug: in.Slug, Name: strings.TrimSpace(in.Name),
			Description: in.Description, Position: position,
		})
		if err != nil {
			return mapCategoryErr(err)
		}
		return s.recordChange(ctx, tx, p, "category.create", id, []string{path}, in)
	})
	if err != nil {
		return Category{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) positionOrNext(ctx context.Context, q *taxonomydb.Queries, parentID *int64, pos *float64) (float64, error) {
	if pos != nil {
		return *pos, nil
	}
	return q.NextChildPosition(ctx, parentID)
}

func (s *Service) Update(ctx context.Context, p auth.Principal, id int64, in UpdateCategory) (Category, error) {
	fields := map[string]string{}
	validateName(fields, in.Name)
	if len(in.SEO) == 0 {
		in.SEO = json.RawMessage("{}")
	} else if !json.Valid(in.SEO) || in.SEO[0] != '{' {
		fields["seo"] = "seo must be a JSON object."
	}
	if len(fields) > 0 {
		return Category{}, apperr.ValidationFailed(fields)
	}
	if err := s.policy.Require(ctx, p, access.RoleEditor, &id); err != nil {
		return Category{}, err
	}
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		c, err := q.GetCategoryForUpdate(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("category.not_found", "No such category.")
		}
		if err != nil {
			return err
		}
		if err := q.UpdateCategoryDetails(ctx, taxonomydb.UpdateCategoryDetailsParams{
			ID: id, Name: strings.TrimSpace(in.Name), Description: in.Description, Seo: in.SEO, Position: in.Position,
		}); err != nil {
			return err
		}
		return s.recordChange(ctx, tx, p, "category.update", id, []string{c.Path}, in)
	})
	if err != nil {
		return Category{}, err
	}
	return s.Get(ctx, id)
}

// Move re-parents and/or renames a category. Every descendant's path is rewritten in the same
// transaction, so the tree is never observed half-moved.
//
// Content URLs are /{rootSlug}/{contentSlug}; the web app resolves content by its own slug and
// 301s a stale section prefix to the canonical one, so moving a section never breaks links.
func (s *Service) Move(ctx context.Context, p auth.Principal, id int64, in MoveCategory) (Category, error) {
	fields := map[string]string{}
	validateSlug(fields, in.Slug)
	if len(fields) > 0 {
		return Category{}, apperr.ValidationFailed(fields)
	}
	if err := s.policy.Require(ctx, p, access.RoleEditor, &id); err != nil {
		return Category{}, err
	}
	if in.ParentID == nil {
		if reservedRootSlugs[in.Slug] {
			return Category{}, apperr.ValidationFailed(map[string]string{"slug": "This slug is reserved for a site page."})
		}
		if err := s.policy.Require(ctx, p, access.RoleAdmin, nil); err != nil {
			return Category{}, err
		}
	} else if err := s.policy.Require(ctx, p, access.RoleEditor, in.ParentID); err != nil {
		return Category{}, err
	}

	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		c, err := q.GetCategoryForUpdate(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("category.not_found", "No such category.")
		}
		if err != nil {
			return err
		}
		newPath := in.Slug
		if in.ParentID != nil {
			parent, err := q.GetCategory(ctx, *in.ParentID)
			if db.IsNoRows(err) {
				return apperr.ValidationFailed(map[string]string{"parentId": "No such category."})
			}
			if err != nil {
				return err
			}
			if parent.Path == c.Path || strings.HasPrefix(parent.Path, c.Path+".") {
				return apperr.Invalid("category.move_into_self", "A category cannot move inside itself.")
			}
			newPath = parent.Path + "." + in.Slug
		}
		if newPath == c.Path {
			return nil
		}
		if err := checkSubtreeDepth(ctx, q, c.Path, newPath); err != nil {
			return err
		}
		if _, err := q.RewriteSubtreePaths(ctx, taxonomydb.RewriteSubtreePathsParams{OldPath: c.Path, NewPath: newPath}); err != nil {
			return mapCategoryErr(err)
		}
		if err := q.SetCategoryParentAndSlug(ctx, taxonomydb.SetCategoryParentAndSlugParams{ParentID: in.ParentID, Slug: in.Slug, ID: id}); err != nil {
			return mapCategoryErr(err)
		}
		return s.recordChange(ctx, tx, p, "category.move", id, []string{c.Path, newPath},
			map[string]string{"from": c.Path, "to": newPath})
	})
	if err != nil {
		return Category{}, err
	}
	return s.Get(ctx, id)
}

// checkSubtreeDepth rejects a move that would push the deepest descendant past MaxDepth.
func checkSubtreeDepth(ctx context.Context, q *taxonomydb.Queries, oldPath, newPath string) error {
	deepest, err := q.MaxSubtreeDepth(ctx, oldPath)
	if err != nil {
		return err
	}
	oldLevels, newLevels := strings.Count(oldPath, ".")+1, strings.Count(newPath, ".")+1
	if int(deepest)-oldLevels+newLevels > MaxDepth {
		return apperr.Invalid("category.too_deep", fmt.Sprintf("Categories nest at most %d levels.", MaxDepth))
	}
	return nil
}

// Delete removes an empty category. Anything with children or content must be emptied or
// moved first, so a delete can never silently orphan published pages.
func (s *Service) Delete(ctx context.Context, p auth.Principal, id int64) error {
	if err := s.policy.Require(ctx, p, access.RoleEditor, &id); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := taxonomydb.New(tx)
		c, err := q.GetCategoryForUpdate(ctx, id)
		if db.IsNoRows(err) {
			return apperr.NotFound("category.not_found", "No such category.")
		}
		if err != nil {
			return err
		}
		inUse, err := q.CategoryInUse(ctx, id)
		if err != nil {
			return err
		}
		if inUse {
			return apperr.Conflict("category.not_empty", "Move or delete its subcategories and content first.")
		}
		if err := q.DeleteCategory(ctx, id); err != nil {
			return err
		}
		return s.recordChange(ctx, tx, p, "category.delete", id, []string{c.Path}, map[string]string{"path": c.Path})
	})
}

func (s *Service) recordChange(ctx context.Context, tx pgx.Tx, p auth.Principal, action string, id int64, paths []string, data any) error {
	if err := audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: action, Entity: "category", EntityID: id, Data: data}); err != nil {
		return err
	}
	urlPaths := make([]string, len(paths))
	for i, pth := range paths {
		urlPaths[i] = toURLPath(pth)
	}
	return outbox.Publish(ctx, tx, outbox.TopicCategoryChanged, id, outbox.CategoryEvent{Paths: urlPaths})
}

func mapCategoryErr(err error) error {
	switch c, _ := db.ConstraintViolation(err); c {
	case "category_path_key", "category_parent_id_slug_key":
		return apperr.Conflict("category.slug_taken", "A category with this slug already exists here.")
	}
	return err
}

// CategoriesByID batch-loads categories (missing ids are absent), for listings that show
// each item's section.
func (s *Service) CategoriesByID(ctx context.Context, ids []int64) (map[int64]Category, error) {
	out := make(map[int64]Category, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := taxonomydb.New(s.db.Replica).ListCategoriesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = fromRow(r)
	}
	return out, nil
}

// Trail returns the root-first breadcrumb ending at the category itself.
func (s *Service) Trail(ctx context.Context, id int64) ([]Category, error) {
	q := taxonomydb.New(s.db.Replica)
	c, err := q.GetCategory(ctx, id)
	if db.IsNoRows(err) {
		return []Category{}, nil
	}
	if err != nil {
		return nil, err
	}
	ancestors, err := q.ListCategoryAncestors(ctx, c.Path)
	if err != nil {
		return nil, err
	}
	return append(fromRows(ancestors), fromRow(c)), nil
}

// ByURLPath resolves "dsa/algorithms" to its category (for listing filters).
func (s *Service) ByURLPath(ctx context.Context, urlPath string) (Category, error) {
	notFound := apperr.NotFound("category.not_found", "No such category.")
	path, ok := toLtree(urlPath)
	if !ok {
		return Category{}, notFound
	}
	c, err := taxonomydb.New(s.db.Replica).GetCategoryByPath(ctx, path)
	if db.IsNoRows(err) {
		return Category{}, notFound
	}
	if err != nil {
		return Category{}, err
	}
	return fromRow(c), nil
}
