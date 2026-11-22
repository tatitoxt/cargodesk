package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tatitoxt/cargodesk/api/internal/domain"
	"github.com/tatitoxt/cargodesk/api/internal/sla"
)

// ErrSlugTaken is returned when the requested organization slug already exists.
var ErrSlugTaken = errors.New("an organization with this slug already exists")

var orgSlugRe = regexp.MustCompile(`^[a-z0-9-]{2,40}$`)
var refPrefixRe = regexp.MustCompile(`^[A-Z]{2,4}$`)

// SignupInput describes a self-service organization registration.
type SignupInput struct {
	Slug      string
	Name      string
	Timezone  string
	Prefix    string
	Email     string
	AdminName string
	Password  string
}

// Signup provisions a new organization with its default SLA policies and its
// first admin account in a single transaction, and returns the admin's user id
// so the caller can issue a session immediately. Email uniqueness is global —
// LoginByPassword resolves users by email alone, so one email must not straddle
// two organizations.
func (s *Store) Signup(ctx context.Context, in SignupInput) (userID string, err error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.Prefix = strings.ToUpper(strings.TrimSpace(in.Prefix))
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.AdminName = strings.TrimSpace(in.AdminName)

	var fields []domain.FieldError
	if !orgSlugRe.MatchString(in.Slug) {
		fields = append(fields, domain.FieldError{Field: "slug", Message: "must be 2-40 chars of lowercase letters, digits and dashes"})
	}
	if in.Name == "" || len([]rune(in.Name)) > 120 {
		fields = append(fields, domain.FieldError{Field: "name", Message: "is required (max 120 characters)"})
	}
	if _, tzErr := time.LoadLocation(in.Timezone); tzErr != nil {
		fields = append(fields, domain.FieldError{Field: "timezone", Message: "must be a valid IANA timezone (e.g. America/Argentina/Buenos_Aires)"})
	}
	if !refPrefixRe.MatchString(in.Prefix) {
		fields = append(fields, domain.FieldError{Field: "reference_prefix", Message: "must be 2-4 uppercase letters (e.g. CD)"})
	}
	if !strings.Contains(in.Email, "@") || len(in.Email) > 254 {
		fields = append(fields, domain.FieldError{Field: "email", Message: "must be a valid email address"})
	}
	if in.AdminName == "" || len([]rune(in.AdminName)) > 120 {
		fields = append(fields, domain.FieldError{Field: "display_name", Message: "is required (max 120 characters)"})
	}
	if len(in.Password) < minPasswordLen {
		fields = append(fields, domain.FieldError{Field: "password", Message: "must be at least 10 characters"})
	}
	if len(fields) > 0 {
		return "", &domain.ValidationError{Fields: fields}
	}

	hash, err := hashPassword(in.Password)
	if err != nil {
		return "", err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orgID string
	err = tx.QueryRow(ctx,
		`INSERT INTO organizations (slug, name, timezone, reference_prefix) VALUES ($1,$2,$3,$4) RETURNING id`,
		in.Slug, in.Name, in.Timezone, in.Prefix).Scan(&orgID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrSlugTaken
	}
	if err != nil {
		return "", err
	}

	for pr, hours := range sla.DefaultPolicy() {
		if _, err = tx.Exec(ctx,
			`INSERT INTO sla_policies (organization_id, priority, hours) VALUES ($1,$2,$3)`,
			orgID, string(pr), hours); err != nil {
			return "", err
		}
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO users (organization_id, email, display_name, role, password_hash, must_change_password)
		 VALUES ($1,$2,$3,$4,$5,false) RETURNING id`,
		orgID, in.Email, in.AdminName, string(domain.Admin), hash).Scan(&userID)
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", ErrEmailTaken
	}
	if err != nil {
		return "", err
	}
	return userID, tx.Commit(ctx)
}
