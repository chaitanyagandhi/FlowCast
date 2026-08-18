package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

func writeDotEnv(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, ".env")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestLoadDotEnvFromWalksUpToFindFile(t *testing.T) {
	isolateEnv(t)

	root := t.TempDir()
	want := writeDotEnv(t, root, "FLOWCAST_HTTP_PORT=4321\n")

	nested := filepath.Join(root, "backend", "internal", "config")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	got, err := config.LoadDotEnvFrom(nested)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, "4321", os.Getenv("FLOWCAST_HTTP_PORT"))
}

// The environment is authoritative: a .env file supplies local defaults only. This is what
// keeps a container's injected secrets from being clobbered by a stray checked-out file.
func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	isolateEnv(t)
	t.Setenv("FLOWCAST_HTTP_PORT", "9000")

	dir := t.TempDir()
	writeDotEnv(t, dir, "FLOWCAST_HTTP_PORT=4321\n")

	_, err := config.LoadDotEnvFrom(dir)
	require.NoError(t, err)
	require.Equal(t, "9000", os.Getenv("FLOWCAST_HTTP_PORT"))
}

func TestLoadDotEnvFromReportsMissingFile(t *testing.T) {
	isolateEnv(t)

	// t.TempDir sits under the OS temp root, which has no .env above it.
	_, err := config.LoadDotEnvFrom(t.TempDir())
	require.ErrorIs(t, err, config.ErrNoDotEnv)
}

// A missing .env must be recoverable, not fatal: production supplies real environment
// variables and has no file at all.
func TestLoadSucceedsWithoutDotEnv(t *testing.T) {
	isolateEnv(t)

	_, err := config.LoadDotEnvFrom(t.TempDir())
	require.ErrorIs(t, err, config.ErrNoDotEnv)

	setRequired(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 8080, cfg.Server.Port)
}
