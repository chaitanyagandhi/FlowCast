package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
)

// Pagination bounds for incident listings.
const (
	DefaultIncidentLimit = 25
	MaxIncidentLimit     = 100
)

// incidentColumns is the projection used everywhere an incident is read.
//
// embedding is deliberately absent. It is 1536 floats per row, useful only to similarity
// search, and fetching it on every list would dominate the response for no benefit. The
// search path reads it explicitly.
const incidentColumns = `
	id, team_id, external_id, title, description, severity, status, source,
	started_at, resolved_at, metadata, created_at, updated_at`

// IncidentRepository reads and writes incidents.
//
// Every method takes a team id and puts it in the WHERE clause. Ownership is never checked
// in Go after the fact: a row belonging to another team is not fetched and then rejected,
// it is never selected at all. That means a missing incident and someone else's incident
// are indistinguishable, which is the point -- a caller must not be able to probe for
// which ids exist.
type IncidentRepository struct {
	pool *pgxpool.Pool
}

// NewIncidentRepository builds a repository over a connection pool.
func NewIncidentRepository(pool *pgxpool.Pool) *IncidentRepository {
	return &IncidentRepository{pool: pool}
}

// IncidentFilter narrows a listing. Empty slices mean "no constraint".
type IncidentFilter struct {
	Status   []models.IncidentStatus
	Severity []models.Severity
	Source   []models.Source

	Limit  int
	Offset int
}

// IncidentPage is one page of results plus the total matching the filter, so a client can
// render "showing 25 of 312" without a second request.
type IncidentPage struct {
	Incidents []models.Incident
	Total     int
}

// IncidentPatch describes a partial update. A nil field is left alone.
type IncidentPatch struct {
	Title       *string
	Description *string
	Severity    *models.Severity
	Status      *models.IncidentStatus
	ResolvedAt  *time.Time

	// ClearResolvedAt reopens an incident. It is separate from ResolvedAt because a nil
	// pointer already means "leave unchanged", so there would otherwise be no way to
	// express "set this back to null".
	ClearResolvedAt bool
}

// Create inserts a new incident.
//
// The embedding column is not written here. An incident's embedding is derived from its
// analysis, which does not exist yet at creation time.
func (r *IncidentRepository) Create(ctx context.Context, incident models.Incident) (models.Incident, error) {
	const query = `
		INSERT INTO incidents
			(team_id, external_id, title, description, severity, status, source,
			 started_at, resolved_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb))
		RETURNING` + incidentColumns

	row := r.pool.QueryRow(ctx, query,
		incident.TeamID,
		incident.ExternalID,
		strings.TrimSpace(incident.Title),
		incident.Description,
		incident.Severity,
		incident.Status,
		incident.Source,
		incident.StartedAt,
		incident.ResolvedAt,
		incident.Metadata,
	)

	created, err := scanIncident(row)
	if err != nil {
		if isUniqueViolation(err) {
			// Same external id, same team: a redelivered webhook, not a new incident.
			return models.Incident{}, fmt.Errorf("creating incident: %w", models.ErrConflict)
		}
		return models.Incident{}, fmt.Errorf("creating incident: %w", err)
	}
	return created, nil
}

// GetByID returns one of the team's incidents.
func (r *IncidentRepository) GetByID(ctx context.Context, teamID, id uuid.UUID) (models.Incident, error) {
	const query = `SELECT` + incidentColumns + `
		FROM incidents
		WHERE id = $1 AND team_id = $2`

	incident, err := scanIncident(r.pool.QueryRow(ctx, query, id, teamID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Incident{}, models.ErrNotFound
		}
		return models.Incident{}, fmt.Errorf("getting incident: %w", err)
	}
	return incident, nil
}

// FindByExternalID looks up an incident by the identifier its source system gave it,
// which is how a redelivered webhook attaches to the incident it already created.
func (r *IncidentRepository) FindByExternalID(ctx context.Context, teamID uuid.UUID, externalID string) (models.Incident, error) {
	const query = `SELECT` + incidentColumns + `
		FROM incidents
		WHERE team_id = $1 AND external_id = $2`

	incident, err := scanIncident(r.pool.QueryRow(ctx, query, teamID, externalID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Incident{}, models.ErrNotFound
		}
		return models.Incident{}, fmt.Errorf("finding incident by external id: %w", err)
	}
	return incident, nil
}

// List returns a page of the team's incidents, newest first.
func (r *IncidentRepository) List(ctx context.Context, teamID uuid.UUID, filter IncidentFilter) (IncidentPage, error) {
	limit, offset := filter.bounds()

	// count(*) OVER () rides along with the page, so the total costs no extra round trip.
	query := `
		SELECT` + incidentColumns + `, count(*) OVER () AS total
		FROM incidents
		WHERE team_id = $1`

	args := []any{teamID}

	if len(filter.Status) > 0 {
		args = append(args, enumStrings(filter.Status))
		query += fmt.Sprintf(" AND status = ANY($%d::text[])", len(args))
	}
	if len(filter.Severity) > 0 {
		args = append(args, enumStrings(filter.Severity))
		query += fmt.Sprintf(" AND severity = ANY($%d::text[])", len(args))
	}
	if len(filter.Source) > 0 {
		args = append(args, enumStrings(filter.Source))
		query += fmt.Sprintf(" AND source = ANY($%d::text[])", len(args))
	}

	// id breaks ties so paging is stable when incidents share a start time.
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY started_at DESC, id DESC LIMIT $%d OFFSET $%d",
		len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return IncidentPage{}, fmt.Errorf("listing incidents: %w", err)
	}
	defer rows.Close()

	page := IncidentPage{Incidents: []models.Incident{}}
	for rows.Next() {
		var incident models.Incident
		var total int
		if err := scanIncidentInto(rows, &incident, &total); err != nil {
			return IncidentPage{}, fmt.Errorf("scanning incident: %w", err)
		}
		page.Incidents = append(page.Incidents, incident)
		page.Total = total
	}
	if err := rows.Err(); err != nil {
		return IncidentPage{}, fmt.Errorf("listing incidents: %w", err)
	}

	return page, nil
}

// Update applies a partial change to one of the team's incidents.
func (r *IncidentRepository) Update(ctx context.Context, teamID, id uuid.UUID, patch IncidentPatch) (models.Incident, error) {
	assignments := make([]string, 0, 6)
	args := make([]any, 0, 8)

	// Column names come from this fixed set, never from a caller, and every value is a
	// bound parameter.
	set := func(column string, value any) {
		args = append(args, value)
		assignments = append(assignments, fmt.Sprintf("%s = $%d", column, len(args)))
	}

	if patch.Title != nil {
		set("title", strings.TrimSpace(*patch.Title))
	}
	if patch.Description != nil {
		set("description", *patch.Description)
	}
	if patch.Severity != nil {
		set("severity", *patch.Severity)
	}
	if patch.Status != nil {
		set("status", *patch.Status)
	}
	switch {
	case patch.ClearResolvedAt:
		assignments = append(assignments, "resolved_at = NULL")
	case patch.ResolvedAt != nil:
		set("resolved_at", *patch.ResolvedAt)
	}

	if len(assignments) == 0 {
		// Nothing to change; return the current row rather than a pointless write.
		return r.GetByID(ctx, teamID, id)
	}

	args = append(args, id, teamID)
	query := fmt.Sprintf(`
		UPDATE incidents SET %s
		WHERE id = $%d AND team_id = $%d
		RETURNING %s`,
		strings.Join(assignments, ", "), len(args)-1, len(args), incidentColumns)

	incident, err := scanIncident(r.pool.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either no such incident, or it belongs to another team. The caller is not
			// told which.
			return models.Incident{}, models.ErrNotFound
		}
		return models.Incident{}, fmt.Errorf("updating incident: %w", err)
	}
	return incident, nil
}

// Delete removes one of the team's incidents, taking its events and analyses with it.
func (r *IncidentRepository) Delete(ctx context.Context, teamID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM incidents WHERE id = $1 AND team_id = $2`, id, teamID)
	if err != nil {
		return fmt.Errorf("deleting incident: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// bounds clamps pagination so a caller cannot ask for the whole table, and so a missing
// limit yields a sensible page rather than none.
func (f IncidentFilter) bounds() (limit, offset int) {
	limit = f.Limit
	switch {
	case limit <= 0:
		limit = DefaultIncidentLimit
	case limit > MaxIncidentLimit:
		limit = MaxIncidentLimit
	}

	offset = f.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// row is the part of pgx.Row and pgx.Rows this file needs.
type row interface {
	Scan(dest ...any) error
}

func scanIncident(r row) (models.Incident, error) {
	var incident models.Incident
	err := r.Scan(
		&incident.ID, &incident.TeamID, &incident.ExternalID, &incident.Title,
		&incident.Description, &incident.Severity, &incident.Status, &incident.Source,
		&incident.StartedAt, &incident.ResolvedAt, &incident.Metadata,
		&incident.CreatedAt, &incident.UpdatedAt,
	)
	return incident, err
}

func scanIncidentInto(r row, incident *models.Incident, total *int) error {
	return r.Scan(
		&incident.ID, &incident.TeamID, &incident.ExternalID, &incident.Title,
		&incident.Description, &incident.Severity, &incident.Status, &incident.Source,
		&incident.StartedAt, &incident.ResolvedAt, &incident.Metadata,
		&incident.CreatedAt, &incident.UpdatedAt, total,
	)
}

// enumStrings converts a slice of string-backed enums for use as a SQL text array.
func enumStrings[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
