package models_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/models"
	"github.com/chaitanyagandhi/flowcast/backend/migrations"
)

// A Go enumeration and its SQL CHECK constraint are two copies of the same decision, and
// copies drift. Adding a status in Go without adding it to the migration produces a
// constraint violation at runtime; removing one from Go leaves dead values in the
// database. These tests compare the two directly so the mismatch fails the build instead.

// quotedValue matches a single-quoted SQL literal.
var quotedValue = regexp.MustCompile(`'([^']*)'`)

// checkConstraintValues extracts the allowed values from every `<column> IN (...)` clause
// for a column in one migration file, and requires all occurrences to agree.
func checkConstraintValues(t *testing.T, filename, column string) []string {
	t.Helper()

	content, err := migrations.FS.ReadFile(filename)
	require.NoError(t, err, "reading %s", filename)
	sql := string(content)

	needle := column + " IN ("
	var sets [][]string

	for offset := 0; ; {
		start := strings.Index(sql[offset:], needle)
		if start == -1 {
			break
		}
		start += offset + len(needle)

		end := strings.Index(sql[start:], ")")
		require.NotEqual(t, -1, end, "unterminated IN clause for %s in %s", column, filename)

		var values []string
		for _, m := range quotedValue.FindAllStringSubmatch(sql[start:start+end], -1) {
			values = append(values, m[1])
		}
		require.NotEmpty(t, values, "empty IN clause for %s in %s", column, filename)
		sets = append(sets, values)

		offset = start + end
	}

	require.NotEmpty(t, sets,
		"found no CHECK (%s IN (...)) in %s", column, filename)

	// The same column constrained in two tables must be constrained identically.
	for i := 1; i < len(sets); i++ {
		require.Equal(t, sets[0], sets[i],
			"%s is constrained inconsistently within %s", column, filename)
	}
	return sets[0]
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func TestGoEnumsMatchDatabaseConstraints(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		column   string
		goValues []string
	}{
		{
			name: "provider", file: "0002_identity.sql", column: "provider",
			goValues: enumToStrings(models.AllProviders()),
		},
		{
			name: "severity", file: "0003_incidents.sql", column: "severity",
			goValues: enumToStrings(models.AllSeverities()),
		},
		{
			name: "incident status", file: "0003_incidents.sql", column: "status",
			goValues: enumToStrings(models.AllIncidentStatuses()),
		},
		{
			// Constrained on both incidents and events; the helper requires the two
			// clauses to be identical, which is itself worth checking.
			name: "source", file: "0003_incidents.sql", column: "source",
			goValues: enumToStrings(models.AllSources()),
		},
		{
			name: "classification", file: "0003_incidents.sql", column: "classification",
			goValues: enumToStrings(models.AllClassifications()),
		},
		{
			name: "run status", file: "0004_simulation.sql", column: "status",
			goValues: enumToStrings(models.AllRunStatuses()),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sqlValues := checkConstraintValues(t, tc.file, tc.column)
			require.Equal(t, sorted(tc.goValues), sorted(sqlValues),
				"Go enum and the CHECK constraint in %s disagree", tc.file)
		})
	}
}

// MaxHypotheses is duplicated as a rank CHECK in SQL; the two must say the same thing.
func TestHypothesisLimitMatchesDatabase(t *testing.T) {
	content, err := migrations.FS.ReadFile("0003_incidents.sql")
	require.NoError(t, err)

	require.Contains(t, string(content),
		fmt.Sprintf("rank BETWEEN 1 AND %d", models.MaxHypotheses))
}

// The webhook secret minimum is enforced in Go before insert and in SQL as a backstop.
func TestWebhookSecretMinimumMatchesDatabase(t *testing.T) {
	content, err := migrations.FS.ReadFile("0002_identity.sql")
	require.NoError(t, err)

	require.Contains(t, string(content),
		fmt.Sprintf("length(webhook_secret) >= %d", models.MinWebhookSecretLen))
}

// enumToStrings converts any string-backed enum slice for comparison.
func enumToStrings[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
