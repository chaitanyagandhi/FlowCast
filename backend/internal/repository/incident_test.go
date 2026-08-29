package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
	"github.com/chaitanyagandhi/flowcast/backend/internal/repository"
)

// twoTeams sets up two tenants sharing one database, which is the situation every
// isolation test needs.
type twoTeams struct {
	repo *repository.IncidentRepository
	a    uuid.UUID
	b    uuid.UUID
}

func setupTeams(t *testing.T) twoTeams {
	t.Helper()
	pool := migratedSchema(t)
	users := repository.NewUserRepository(pool)
	ctx := context.Background()

	teamA, _, err := users.CreateTeamWithOwner(ctx, "Team A", newUser("a@example.com"))
	require.NoError(t, err)
	teamB, _, err := users.CreateTeamWithOwner(ctx, "Team B", newUser("b@example.com"))
	require.NoError(t, err)

	return twoTeams{repo: repository.NewIncidentRepository(pool), a: teamA.ID, b: teamB.ID}
}

func newIncident(teamID uuid.UUID, title string) models.Incident {
	return models.Incident{
		TeamID:    teamID,
		Title:     title,
		Severity:  models.SeverityP1,
		Status:    models.StatusOpen,
		Source:    models.SourcePagerDuty,
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}
}

func ptr[T any](v T) *T { return &v }

func TestCreateAndGetIncident(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	incident := newIncident(teams.a, "Checkout latency spike")
	incident.Description = "Checkout p99 above 3s"
	incident.Metadata = map[string]any{"region": "us-east-1"}

	created, err := teams.repo.Create(ctx, incident)
	require.NoError(t, err)

	require.NotEqual(t, uuid.Nil, created.ID)
	require.Equal(t, teams.a, created.TeamID)
	require.Equal(t, "Checkout latency spike", created.Title)
	require.Equal(t, models.StatusOpen, created.Status)
	require.Equal(t, "us-east-1", created.Metadata["region"])
	require.False(t, created.CreatedAt.IsZero())

	found, err := teams.repo.GetByID(ctx, teams.a, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, found.ID)
	require.Equal(t, created.Title, found.Title)
}

// The central tenancy guarantee: another team's incident is not fetched and then
// rejected, it is never selected. A caller cannot tell it apart from one that does not
// exist, so ids cannot be probed.
func TestOneTeamCannotReadAnothersIncident(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Team A incident"))
	require.NoError(t, err)

	_, err = teams.repo.GetByID(ctx, teams.b, created.ID)
	require.ErrorIs(t, err, models.ErrNotFound)

	// And the same error a genuinely absent incident produces.
	_, missingErr := teams.repo.GetByID(ctx, teams.b, uuid.New())
	require.ErrorIs(t, missingErr, models.ErrNotFound)
	require.Equal(t, err.Error(), missingErr.Error(),
		"the two cases must be indistinguishable")
}

func TestOneTeamCannotUpdateAnothersIncident(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Team A incident"))
	require.NoError(t, err)

	_, err = teams.repo.Update(ctx, teams.b, created.ID,
		repository.IncidentPatch{Title: ptr("Hijacked")})
	require.ErrorIs(t, err, models.ErrNotFound)

	// The row is untouched.
	unchanged, err := teams.repo.GetByID(ctx, teams.a, created.ID)
	require.NoError(t, err)
	require.Equal(t, "Team A incident", unchanged.Title)
}

func TestOneTeamCannotDeleteAnothersIncident(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Team A incident"))
	require.NoError(t, err)

	require.ErrorIs(t, teams.repo.Delete(ctx, teams.b, created.ID), models.ErrNotFound)

	_, err = teams.repo.GetByID(ctx, teams.a, created.ID)
	require.NoError(t, err, "the incident must survive another team's delete")
}

func TestListReturnsOnlyTheCallersTeam(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	for _, title := range []string{"A one", "A two"} {
		_, err := teams.repo.Create(ctx, newIncident(teams.a, title))
		require.NoError(t, err)
	}
	_, err := teams.repo.Create(ctx, newIncident(teams.b, "B one"))
	require.NoError(t, err)

	page, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{})
	require.NoError(t, err)

	require.Len(t, page.Incidents, 2)
	require.Equal(t, 2, page.Total)
	for _, incident := range page.Incidents {
		require.Equal(t, teams.a, incident.TeamID)
		require.NotEqual(t, "B one", incident.Title)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)

	for i, title := range []string{"oldest", "middle", "newest"} {
		incident := newIncident(teams.a, title)
		incident.StartedAt = base.Add(time.Duration(i) * time.Hour)
		_, err := teams.repo.Create(ctx, incident)
		require.NoError(t, err)
	}

	page, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{})
	require.NoError(t, err)

	require.Equal(t, []string{"newest", "middle", "oldest"},
		[]string{page.Incidents[0].Title, page.Incidents[1].Title, page.Incidents[2].Title})
}

func TestListPaginates(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)

	for i := range 7 {
		incident := newIncident(teams.a, "incident")
		incident.StartedAt = base.Add(time.Duration(i) * time.Minute)
		_, err := teams.repo.Create(ctx, incident)
		require.NoError(t, err)
	}

	first, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{Limit: 3})
	require.NoError(t, err)
	require.Len(t, first.Incidents, 3)
	require.Equal(t, 7, first.Total, "the total counts every match, not just this page")

	second, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{Limit: 3, Offset: 3})
	require.NoError(t, err)
	require.Len(t, second.Incidents, 3)
	require.Equal(t, 7, second.Total)

	last, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{Limit: 3, Offset: 6})
	require.NoError(t, err)
	require.Len(t, last.Incidents, 1)

	// No incident appears on two pages.
	seen := map[uuid.UUID]bool{}
	for _, page := range []repository.IncidentPage{first, second, last} {
		for _, incident := range page.Incidents {
			require.False(t, seen[incident.ID], "incident %s appeared twice", incident.ID)
			seen[incident.ID] = true
		}
	}
	require.Len(t, seen, 7)
}

func TestListClampsPageSize(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	for range 3 {
		_, err := teams.repo.Create(ctx, newIncident(teams.a, "incident"))
		require.NoError(t, err)
	}

	// A caller asking for everything gets the ceiling, not the table.
	page, err := teams.repo.List(ctx, teams.a,
		repository.IncidentFilter{Limit: 100_000, Offset: -5})
	require.NoError(t, err)
	require.Len(t, page.Incidents, 3)

	// Zero means "unspecified", not "none".
	page, err = teams.repo.List(ctx, teams.a, repository.IncidentFilter{Limit: 0})
	require.NoError(t, err)
	require.Len(t, page.Incidents, 3)
}

func TestListFilters(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	resolved := newIncident(teams.a, "resolved p2 datadog")
	resolved.Status = models.StatusResolved
	resolved.Severity = models.SeverityP2
	resolved.Source = models.SourceDatadog
	resolved.ResolvedAt = ptr(time.Now().UTC())
	_, err := teams.repo.Create(ctx, resolved)
	require.NoError(t, err)

	_, err = teams.repo.Create(ctx, newIncident(teams.a, "open p1 pagerduty"))
	require.NoError(t, err)

	tests := map[string]struct {
		filter    repository.IncidentFilter
		wantTitle string
	}{
		"status":   {repository.IncidentFilter{Status: []models.IncidentStatus{models.StatusResolved}}, "resolved p2 datadog"},
		"severity": {repository.IncidentFilter{Severity: []models.Severity{models.SeverityP1}}, "open p1 pagerduty"},
		"source":   {repository.IncidentFilter{Source: []models.Source{models.SourceDatadog}}, "resolved p2 datadog"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			page, err := teams.repo.List(ctx, teams.a, tc.filter)
			require.NoError(t, err)
			require.Len(t, page.Incidents, 1)
			require.Equal(t, tc.wantTitle, page.Incidents[0].Title)
			require.Equal(t, 1, page.Total)
		})
	}

	t.Run("combined", func(t *testing.T) {
		page, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{
			Status:   []models.IncidentStatus{models.StatusOpen},
			Severity: []models.Severity{models.SeverityP2},
		})
		require.NoError(t, err)
		require.Empty(t, page.Incidents, "filters must combine with AND")
		require.Zero(t, page.Total)
	})
}

func TestListOfAnEmptyTeamIsEmptyNotNil(t *testing.T) {
	teams := setupTeams(t)

	page, err := teams.repo.List(context.Background(), teams.a, repository.IncidentFilter{})
	require.NoError(t, err)
	require.NotNil(t, page.Incidents, "an empty page should serialize as [] rather than null")
	require.Empty(t, page.Incidents)
	require.Zero(t, page.Total)
}

func TestUpdateChangesOnlyTheGivenFields(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Original title"))
	require.NoError(t, err)

	updated, err := teams.repo.Update(ctx, teams.a, created.ID, repository.IncidentPatch{
		Status: ptr(models.StatusAnalysisReady),
	})
	require.NoError(t, err)

	require.Equal(t, models.StatusAnalysisReady, updated.Status)
	require.Equal(t, "Original title", updated.Title, "an untouched field must not change")
	require.Equal(t, created.Severity, updated.Severity)
	require.True(t, updated.UpdatedAt.After(created.UpdatedAt), "the trigger should fire")
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
}

func TestUpdateResolvesAndReopens(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Flapping incident"))
	require.NoError(t, err)

	resolvedAt := created.StartedAt.Add(30 * time.Minute)
	resolved, err := teams.repo.Update(ctx, teams.a, created.ID, repository.IncidentPatch{
		Status:     ptr(models.StatusResolved),
		ResolvedAt: &resolvedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, resolved.ResolvedAt)
	duration, done := resolved.Duration()
	require.True(t, done)
	require.Equal(t, 30*time.Minute, duration)

	reopened, err := teams.repo.Update(ctx, teams.a, created.ID, repository.IncidentPatch{
		Status:          ptr(models.StatusOpen),
		ClearResolvedAt: true,
	})
	require.NoError(t, err)
	require.Nil(t, reopened.ResolvedAt, "clearing must be expressible, not just setting")
	require.True(t, reopened.IsOpen())
}

func TestUpdateWithNothingToChangeReturnsTheCurrentRow(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Unchanged"))
	require.NoError(t, err)

	same, err := teams.repo.Update(ctx, teams.a, created.ID, repository.IncidentPatch{})
	require.NoError(t, err)
	require.Equal(t, created.ID, same.ID)
	require.Equal(t, created.UpdatedAt, same.UpdatedAt, "an empty patch must not bump updated_at")
}

func TestUpdateOfAMissingIncidentIsNotFound(t *testing.T) {
	teams := setupTeams(t)

	_, err := teams.repo.Update(context.Background(), teams.a, uuid.New(),
		repository.IncidentPatch{Title: ptr("nope")})
	require.ErrorIs(t, err, models.ErrNotFound)
}

// A redelivered webhook must attach to the incident it already created rather than
// producing a second one.
func TestExternalIDIsUniquePerTeam(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	withExternal := func(teamID uuid.UUID, external string) models.Incident {
		incident := newIncident(teamID, "Provider incident")
		incident.ExternalID = &external
		return incident
	}

	_, err := teams.repo.Create(ctx, withExternal(teams.a, "PD-123"))
	require.NoError(t, err)

	_, err = teams.repo.Create(ctx, withExternal(teams.a, "PD-123"))
	require.ErrorIs(t, err, models.ErrConflict)

	// A different team may legitimately have the same provider id.
	_, err = teams.repo.Create(ctx, withExternal(teams.b, "PD-123"))
	require.NoError(t, err)
}

func TestFindByExternalIDIsTeamScoped(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	external := "PD-456"
	incident := newIncident(teams.a, "Provider incident")
	incident.ExternalID = &external
	created, err := teams.repo.Create(ctx, incident)
	require.NoError(t, err)

	found, err := teams.repo.FindByExternalID(ctx, teams.a, external)
	require.NoError(t, err)
	require.Equal(t, created.ID, found.ID)

	_, err = teams.repo.FindByExternalID(ctx, teams.b, external)
	require.ErrorIs(t, err, models.ErrNotFound)

	_, err = teams.repo.FindByExternalID(ctx, teams.a, "PD-does-not-exist")
	require.ErrorIs(t, err, models.ErrNotFound)
}

// Manually created incidents have no external id, and many may coexist.
func TestManyIncidentsWithoutExternalIDCoexist(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	for range 3 {
		incident := newIncident(teams.a, "Manual incident")
		incident.Source = models.SourceManual
		_, err := teams.repo.Create(ctx, incident)
		require.NoError(t, err)
	}

	page, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{})
	require.NoError(t, err)
	require.Len(t, page.Incidents, 3)
}

func TestDeleteRemovesTheIncident(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	created, err := teams.repo.Create(ctx, newIncident(teams.a, "Doomed"))
	require.NoError(t, err)

	require.NoError(t, teams.repo.Delete(ctx, teams.a, created.ID))

	_, err = teams.repo.GetByID(ctx, teams.a, created.ID)
	require.ErrorIs(t, err, models.ErrNotFound)

	require.ErrorIs(t, teams.repo.Delete(ctx, teams.a, created.ID), models.ErrNotFound,
		"deleting twice is not found the second time")
}

// The listing projection omits the embedding column deliberately; this pins that it still
// reads every other field correctly.
func TestListReadsAllProjectedFields(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	external := "PD-789"
	incident := newIncident(teams.a, "Fully populated")
	incident.ExternalID = &external
	incident.Description = "A description"
	incident.Metadata = map[string]any{"service": "checkout"}
	created, err := teams.repo.Create(ctx, incident)
	require.NoError(t, err)

	page, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{})
	require.NoError(t, err)
	require.Len(t, page.Incidents, 1)

	got := page.Incidents[0]
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, external, *got.ExternalID)
	require.Equal(t, "A description", got.Description)
	require.Equal(t, "checkout", got.Metadata["service"])
	require.Nil(t, got.Embedding, "the embedding is not part of the listing projection")
}

// A pool exercised concurrently by two tenants must keep their rows apart.
func TestConcurrentTeamsStaySeparated(t *testing.T) {
	teams := setupTeams(t)
	ctx := context.Background()

	done := make(chan error, 2)
	for _, spec := range []struct {
		team  uuid.UUID
		title string
	}{{teams.a, "from A"}, {teams.b, "from B"}} {
		go func() {
			for range 5 {
				if _, err := teams.repo.Create(ctx, newIncident(spec.team, spec.title)); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	require.NoError(t, <-done)
	require.NoError(t, <-done)

	pageA, err := teams.repo.List(ctx, teams.a, repository.IncidentFilter{Limit: 50})
	require.NoError(t, err)
	require.Equal(t, 5, pageA.Total)
	for _, incident := range pageA.Incidents {
		require.Equal(t, "from A", incident.Title)
	}
}
