package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// UserRepository reads and writes teams and users.
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository builds a repository over a connection pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// NormalizeEmail is the canonical form an address is stored and compared in.
//
// Lowercasing matters because the unique index is on lower(email): storing a mixed-case
// address would still collide, but every lookup would need to remember to fold it. Doing
// it once here means the rest of the system cannot forget.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateTeamWithOwner registers a new team and its first user together.
//
// The two rows are written in one transaction because a team with no members is unusable
// and a user with no team violates the tenancy model; a partial failure must leave
// neither.
func (r *UserRepository) CreateTeamWithOwner(
	ctx context.Context, teamName string, user models.User,
) (models.Team, models.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return models.Team{}, models.User{}, fmt.Errorf("starting registration transaction: %w", err)
	}
	// Rolled back unless the commit below succeeds.
	defer func() { _ = tx.Rollback(ctx) }()

	var team models.Team
	const insertTeam = `
		INSERT INTO teams (name)
		VALUES ($1)
		RETURNING id, name, created_at, updated_at`
	if err := tx.QueryRow(ctx, insertTeam, strings.TrimSpace(teamName)).
		Scan(&team.ID, &team.Name, &team.CreatedAt, &team.UpdatedAt); err != nil {
		return models.Team{}, models.User{}, fmt.Errorf("creating team: %w", err)
	}

	var created models.User
	const insertUser = `
		INSERT INTO users (team_id, email, password_hash, name)
		VALUES ($1, $2, $3, $4)
		RETURNING id, team_id, email, password_hash, name, created_at, updated_at`
	err = tx.QueryRow(ctx, insertUser,
		team.ID, NormalizeEmail(user.Email), user.PasswordHash, strings.TrimSpace(user.Name),
	).Scan(&created.ID, &created.TeamID, &created.Email, &created.PasswordHash,
		&created.Name, &created.CreatedAt, &created.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return models.Team{}, models.User{}, fmt.Errorf("creating user: %w", models.ErrConflict)
		}
		return models.Team{}, models.User{}, fmt.Errorf("creating user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Team{}, models.User{}, fmt.Errorf("committing registration: %w", err)
	}

	return team, created, nil
}

// FindByEmail looks a user up by address, case-insensitively, returning models.ErrNotFound
// when there is none.
func (r *UserRepository) FindByEmail(ctx context.Context, email string) (models.User, error) {
	const query = `
		SELECT id, team_id, email, password_hash, name, created_at, updated_at
		FROM users
		WHERE lower(email) = $1`

	var user models.User
	err := r.pool.QueryRow(ctx, query, NormalizeEmail(email)).
		Scan(&user.ID, &user.TeamID, &user.Email, &user.PasswordHash,
			&user.Name, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.User{}, models.ErrNotFound
		}
		return models.User{}, fmt.Errorf("finding user by email: %w", err)
	}
	return user, nil
}

// FindByID looks a user up by primary key.
func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (models.User, error) {
	const query = `
		SELECT id, team_id, email, password_hash, name, created_at, updated_at
		FROM users
		WHERE id = $1`

	var user models.User
	err := r.pool.QueryRow(ctx, query, id).
		Scan(&user.ID, &user.TeamID, &user.Email, &user.PasswordHash,
			&user.Name, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.User{}, models.ErrNotFound
		}
		return models.User{}, fmt.Errorf("finding user by id: %w", err)
	}
	return user, nil
}

// FindTeamByID looks a team up by primary key.
func (r *UserRepository) FindTeamByID(ctx context.Context, id uuid.UUID) (models.Team, error) {
	const query = `SELECT id, name, created_at, updated_at FROM teams WHERE id = $1`

	var team models.Team
	err := r.pool.QueryRow(ctx, query, id).
		Scan(&team.ID, &team.Name, &team.CreatedAt, &team.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Team{}, models.ErrNotFound
		}
		return models.Team{}, fmt.Errorf("finding team by id: %w", err)
	}
	return team, nil
}

// isUniqueViolation reports whether an error is PostgreSQL rejecting a duplicate.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation
}
