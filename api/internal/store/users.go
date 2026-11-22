package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tatitoxt/cargodesk/api/internal/domain"
)

var (
	// ErrEmailTaken is returned when creating a user whose email already exists.
	ErrEmailTaken = errors.New("a user with this email already exists")
	// ErrLastAdmin guards organization admin demotion/removal.
	ErrLastAdmin = errors.New("the organization must keep at least one admin")
	// ErrUserHasHistory blocks deleting a member referenced by shipments or
	// audit events; demote them to viewer instead.
	ErrUserHasHistory = errors.New("this member has shipment history; change their role instead of removing them")
)

// validEmail accepts a bare addr-spec (no display name) of at most 254 bytes.
func validEmail(email string) bool {
	if len(email) < 3 || len(email) > 254 || strings.ContainsAny(email, " <>") {
		return false
	}
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && a.Name == ""
}

// passwordProblem returns why a new password is unacceptable, or "".
// bcrypt only accepts up to 72 bytes.
func passwordProblem(pw string) string {
	switch {
	case len(pw) < minPasswordLen:
		return "must be at least 10 characters"
	case len(pw) > maxPasswordBytes:
		return "must be at most 72 bytes"
	}
	return ""
}

// CreateUser adds a member to the caller's organization; admin-only callers.
func (s *Store) CreateUser(ctx context.Context, a Actor, email, displayName string, role domain.Role) (User, error) {
	return s.createUser(ctx, a, email, displayName, role, nil)
}

// createUser inserts the member, with a password hash when passwordHash is
// non-nil (invites), in a single statement so no credential-less row is left
// behind on failure.
func (s *Store) createUser(ctx context.Context, a Actor, email, displayName string, role domain.Role, passwordHash []byte) (User, error) {
	var fields []domain.FieldError
	email = strings.ToLower(strings.TrimSpace(email))
	displayName = strings.TrimSpace(displayName)
	if !validEmail(email) {
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
		INSERT INTO users (organization_id, email, display_name, role, password_hash, must_change_password)
		VALUES ($1, $2, $3, $4, $5::bytea, $5::bytea IS NOT NULL)
		RETURNING id`, a.Org.ID, email, displayName, role, passwordHash).Scan(&u.ID)
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
// org. Only the provided fields are written. Demoting the last admin is
// rejected; the org's admin rows are locked so concurrent demotions cannot
// both pass the check.
func (s *Store) UpdateUser(ctx context.Context, a Actor, userID string, role *domain.Role, displayName *string) (User, error) {
	if !domain.IsUUID(userID) {
		return User{}, ErrNotFound
	}
	var fields []domain.FieldError
	if role != nil {
		switch *role {
		case domain.Viewer, domain.Coordinator, domain.Admin:
		default:
			fields = append(fields, domain.FieldError{Field: "role", Message: "must be viewer, coordinator or admin"})
		}
	}
	var name *string
	if displayName != nil {
		n := strings.TrimSpace(*displayName)
		if n == "" || len([]rune(n)) > 120 {
			fields = append(fields, domain.FieldError{Field: "display_name", Message: "is required (max 120 characters)"})
		}
		name = &n
	}
	if len(fields) > 0 {
		return User{}, &domain.ValidationError{Fields: fields}
	}
	var u User
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockAdmins(ctx, tx, a.Org.ID); err != nil {
			return err
		}
		var current domain.Role
		if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE organization_id = $1 AND id = $2 FOR UPDATE`,
			a.Org.ID, userID).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role != nil && current == domain.Admin && *role != domain.Admin {
			if ok, err := anotherAdminExists(ctx, tx, a.Org.ID, userID); err != nil {
				return err
			} else if !ok {
				return ErrLastAdmin
			}
		}
		return tx.QueryRow(ctx, `
			UPDATE users SET role = COALESCE($3, role), display_name = COALESCE($4, display_name)
			WHERE organization_id = $1 AND id = $2
			RETURNING id, email, display_name, role`, a.Org.ID, userID, role, name).
			Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role)
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// DeleteUser removes a member and their sessions. Deleting the last admin is
// rejected, as is deleting a member referenced by shipments or audit events.
func (s *Store) DeleteUser(ctx context.Context, a Actor, userID string) error {
	if !domain.IsUUID(userID) {
		return ErrNotFound
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockAdmins(ctx, tx, a.Org.ID); err != nil {
			return err
		}
		var role domain.Role
		if err := tx.QueryRow(ctx, `SELECT role FROM users WHERE organization_id = $1 AND id = $2 FOR UPDATE`,
			a.Org.ID, userID).Scan(&role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if role == domain.Admin {
			if ok, err := anotherAdminExists(ctx, tx, a.Org.ID, userID); err != nil {
				return err
			} else if !ok {
				return ErrLastAdmin
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE organization_id = $1 AND id = $2`, a.Org.ID, userID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrUserHasHistory
		}
		return err
	})
}

// lockAdmins serializes admin-count checks within an organization.
func lockAdmins(ctx context.Context, q querier, orgID string) error {
	_, err := q.Exec(ctx, `SELECT id FROM users WHERE organization_id = $1 AND role = 'admin' ORDER BY id FOR UPDATE`, orgID)
	return err
}

func anotherAdminExists(ctx context.Context, q querier, orgID, excluding string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM users
			WHERE organization_id = $1 AND role = 'admin' AND id <> $2)`,
		orgID, excluding).Scan(&ok)
	return ok, err
}

var (
	// ErrBadCredentials is returned when password login fails for any reason.
	ErrBadCredentials = errors.New("invalid email or password")
	// ErrWeakPassword is returned when a new password does not meet the minimum.
	ErrWeakPassword = errors.New("password must be at least 10 characters")
)

const (
	minPasswordLen   = 10
	maxPasswordBytes = 72
)

func hashPassword(pw string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
}

func randomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// InviteUser creates a member with a random temporary password and returns the
// plaintext once — it is never stored or returned again. The invited member
// must set a new password on first sign-in.
func (s *Store) InviteUser(ctx context.Context, a Actor, email, displayName string, role domain.Role) (User, string, error) {
	temp, err := randomPassword()
	if err != nil {
		return User{}, "", err
	}
	hash, err := hashPassword(temp)
	if err != nil {
		return User{}, "", err
	}
	u, err := s.createUser(ctx, a, email, displayName, role, hash)
	if err != nil {
		return User{}, "", err
	}
	return u, temp, nil
}

// LoginByPassword verifies an email/password pair and returns the user's id and
// whether a password change is forced. Users without a password (seeded demo
// users, OIDC-only members) and wrong credentials both yield ErrBadCredentials —
// the error does not reveal which emails exist.
func (s *Store) LoginByPassword(ctx context.Context, email, password string) (id string, mustChange bool, err error) {
	var hash []byte
	err = s.pool.QueryRow(ctx,
		`SELECT id, password_hash, must_change_password FROM users WHERE email = $1`,
		strings.ToLower(strings.TrimSpace(email))).Scan(&id, &hash, &mustChange)
	if err != nil {
		return "", false, ErrBadCredentials
	}
	if len(hash) == 0 || bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		return "", false, ErrBadCredentials
	}
	return id, mustChange, nil
}

// ChangePassword verifies the caller's current password and replaces it,
// clearing the forced-change flag set by an invite.
func (s *Store) ChangePassword(ctx context.Context, a Actor, current, next string) error {
	if msg := passwordProblem(next); msg != "" {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "new_password", Message: msg}}}
	}
	if current == next {
		return &domain.ValidationError{Fields: []domain.FieldError{{Field: "new_password", Message: "must differ from the current password"}}}
	}
	var hash []byte
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, a.UserID).Scan(&hash)
	if err != nil {
		return err
	}
	if len(hash) == 0 || bcrypt.CompareHashAndPassword(hash, []byte(current)) != nil {
		return ErrBadCredentials
	}
	nextHash, err := hashPassword(next)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, must_change_password = false WHERE id = $1`,
		a.UserID, nextHash)
	return err
}
