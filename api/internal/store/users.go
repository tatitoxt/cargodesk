package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tatitoxt/cargodesk/api/internal/domain"
)

var (
	// ErrEmailTaken is returned when creating a user whose email already exists.
	ErrEmailTaken = errors.New("a user with this email already exists")
	// ErrLastAdmin guards organization admin demotion/removal.
	ErrLastAdmin = errors.New("the organization must keep at least one admin")
)

// CreateUser adds a member to the caller's organization; admin-only callers.
func (s *Store) CreateUser(ctx context.Context, a Actor, email, displayName string, role domain.Role) (User, error) {
	var fields []domain.FieldError
	email = strings.ToLower(strings.TrimSpace(email))
	displayName = strings.TrimSpace(displayName)
	if !strings.Contains(email, "@") || len(email) > 254 {
		fields = append(fields, domain.FieldError{Field: "email", Message: "must be a valid email address"})
	}
	if displayName == "" || len([]rune(displayName)) > 120 {
		fields = append(fields, domain.FieldError{Field: "display_name", Message: "is required (max 120 characters)"})
	}
	switch role {
	case domain.Viewer, domain.Coordinator, domain.Admin:
	default:
		fields = append(fields, domain.FieldError{Field: "role", Message: "must be viewer, coordinator or admin"})
	}
	if len(fields) > 0 {
		return User{}, &domain.ValidationError{Fields: fields}
	}
	var u User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (organization_id, email, display_name, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, a.Org.ID, email, displayName, role).Scan(&u.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, err
	}
	u.Email, u.DisplayName, u.Role = email, displayName, role
	return u, nil
}

// UpdateUser changes a member's role and/or display name inside the caller's
// org. Demoting the last admin is rejected.
func (s *Store) UpdateUser(ctx context.Context, a Actor, userID string, role *domain.Role, displayName *string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, role FROM users WHERE organization_id = $1 AND id = $2`,
		a.Org.ID, userID).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
	if err != nil {
		return User{}, ErrNotFound
	}
	newRole, newName := u.Role, u.DisplayName
	var fields []domain.FieldError
	if role != nil {
		switch *role {
		case domain.Viewer, domain.Coordinator, domain.Admin:
			newRole = *role
		default:
			fields = append(fields, domain.FieldError{Field: "role", Message: "must be viewer, coordinator or admin"})
		}
	}
	if displayName != nil {
		name := strings.TrimSpace(*displayName)
		if name == "" || len([]rune(name)) > 120 {
			fields = append(fields, domain.FieldError{Field: "display_name", Message: "is required (max 120 characters)"})
		}
		newName = name
	}
	if len(fields) > 0 {
		return User{}, &domain.ValidationError{Fields: fields}
	}
	if u.Role == domain.Admin && newRole != domain.Admin {
		if ok, err := s.anotherAdminExists(ctx, a.Org.ID, u.ID); err != nil {
			return User{}, err
		} else if !ok {
			return User{}, ErrLastAdmin
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE users SET role = $3, display_name = $4
		WHERE organization_id = $1 AND id = $2`, a.Org.ID, userID, newRole, newName)
	if err != nil {
		return User{}, err
	}
	if tag.RowsAffected() == 0 {
		return User{}, ErrNotFound
	}
	u.Role, u.DisplayName = newRole, newName
	return u, nil
}

// DeleteUser removes a member and their sessions. Deleting the last admin is
// rejected.
func (s *Store) DeleteUser(ctx context.Context, a Actor, userID string) error {
	var role domain.Role
	err := s.pool.QueryRow(ctx, `
		SELECT role FROM users WHERE organization_id = $1 AND id = $2`,
		a.Org.ID, userID).Scan(&role)
	if err != nil {
		return ErrNotFound
	}
	if role == domain.Admin {
		if ok, err := s.anotherAdminExists(ctx, a.Org.ID, userID); err != nil {
			return err
		} else if !ok {
			return ErrLastAdmin
		}
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM users WHERE organization_id = $1 AND id = $2`, a.Org.ID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) anotherAdminExists(ctx context.Context, orgID, excluding string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM users
			WHERE organization_id = $1 AND role = 'admin' AND id <> $2)`,
		orgID, excluding).Scan(&ok)
	return ok, err
}
