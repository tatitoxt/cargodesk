package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tatitoxt/cargodesk/api/internal/workflow"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
)

// ErrViewNameTaken is returned when a saved view name already exists in the org.
var ErrViewNameTaken = errors.New("a saved view with this name already exists")

// ViewFilterKeys is the allow-list of board filters a saved view may carry.
var ViewFilterKeys = map[string]bool{
	"state": true, "owner": true, "overdue": true, "due": true, "q": true,
}

type SavedView struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Filters   map[string]string `json:"filters"`
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
}

// ValidateViewFilters rejects filter keys/values the board does not understand.
// viewFilterValueProblem applies the same rules the board's query parser
// does, so a saved view can always be opened.
func viewFilterValueProblem(k, v string) string {
	switch k {
	case "state":
		for _, st := range strings.Split(v, ",") {
			if !workflow.State(strings.TrimSpace(st)).Valid() {
				return "unknown state " + strconv.Quote(st)
			}
		}
	case "owner":
		if v != "me" && v != "none" && !domain.IsUUID(v) {
			return "must be me, none or a user id"
		}
	case "overdue":
		if _, err := strconv.ParseBool(v); err != nil {
			return "must be true or false"
		}
	case "due":
		if v != "today" {
			return `must be "today"`
		}
	}
	return ""
}

func ValidateViewFilters(filters map[string]string) error {
	fields := []domain.FieldError{}
	switch {
	case len(filters) == 0:
		fields = append(fields, domain.FieldError{Field: "filters", Message: "at least one filter is required"})
	case len(filters) > len(ViewFilterKeys):
		fields = append(fields, domain.FieldError{Field: "filters", Message: "too many filters"})
	default:
		for k, v := range filters {
			if !ViewFilterKeys[k] {
				fields = append(fields, domain.FieldError{Field: "filters." + k, Message: "unknown filter key"})
			} else if n := utf8.RuneCountInString(v); n == 0 || n > 200 {
				fields = append(fields, domain.FieldError{Field: "filters." + k, Message: "value must be 1-200 characters"})
			} else if msg := viewFilterValueProblem(k, v); msg != "" {
				fields = append(fields, domain.FieldError{Field: "filters." + k, Message: msg})
			}
		}
	}
	if len(fields) > 0 {
		return &domain.ValidationError{Fields: fields}
	}
	return nil
}

func (s *Store) ListViews(ctx context.Context, a Actor) ([]SavedView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, filters, created_by, created_at
		FROM saved_views WHERE organization_id = $1 ORDER BY name`, a.Org.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedView{}
	for rows.Next() {
		var v SavedView
		var raw []byte
		if err := rows.Scan(&v.ID, &v.Name, &raw, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Filters = map[string]string{}
		if err := json.Unmarshal(raw, &v.Filters); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateView(ctx context.Context, a Actor, name string, filters map[string]string, now time.Time) (SavedView, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 60 {
		return SavedView{}, &domain.ValidationError{Fields: []domain.FieldError{
			{Field: "name", Message: "must be 1-60 characters"},
		}}
	}
	if err := ValidateViewFilters(filters); err != nil {
		return SavedView{}, err
	}
	raw, _ := json.Marshal(filters)
	var v SavedView
	err := s.pool.QueryRow(ctx, `
		INSERT INTO saved_views (organization_id, name, filters, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`, a.Org.ID, name, raw, a.UserID, now).
		Scan(&v.ID, &v.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return SavedView{}, ErrViewNameTaken
	}
	if err != nil {
		return SavedView{}, err
	}
	v.Name, v.Filters, v.CreatedBy = name, filters, a.UserID
	return v, nil
}

func (s *Store) DeleteView(ctx context.Context, a Actor, id string) error {
	if !domain.IsUUID(id) {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM saved_views WHERE organization_id = $1 AND id = $2`, a.Org.ID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
