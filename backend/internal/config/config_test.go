package config_test

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/chaitanyagandhi/flowcast/backend/internal/config"
)

const (
	testDatabaseURL = "postgres://flowcast:secretpw@localhost:5432/flowcast?sslmode=disable"
	testRedisURL    = "redis://localhost:6379/0"
	testJWTSecret   = "0123456789abcdef0123456789abcdef" // exactly 32 chars
)

// isolateEnv removes every variable FlowCast reads so a test never inherits the
// developer's shell or a previously loaded .env. t.Setenv registers the original value,
// so everything is restored when the test finishes.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "FLOWCAST_") || key == "OPENAI_API_KEY" {
			t.Setenv(key, "")
			require.NoError(t, os.Unsetenv(key))
		}
	}
}

// setRequired supplies the minimum valid configuration.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWCAST_DATABASE_URL", testDatabaseURL)
	t.Setenv("FLOWCAST_REDIS_URL", testRedisURL)
	t.Setenv("FLOWCAST_JWT_SECRET", testJWTSecret)
}

// requireInvalid asserts Load failed and returns the offending variable names.
func requireInvalid(t *testing.T) []string {
	t.Helper()
	cfg, err := config.Load()
	require.Error(t, err)
	require.Nil(t, cfg)

	var verr *config.ValidationError
	require.ErrorAs(t, err, &verr)
	return verr.Variables()
}

func TestLoadAppliesDefaults(t *testing.T) {
	isolateEnv(t)
	setRequired(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, config.EnvDevelopment, cfg.Env)
	require.False(t, cfg.Env.IsProduction())

	require.Equal(t, 8080, cfg.Server.Port)
	require.Equal(t, slog.LevelInfo, cfg.Server.LogLevel)
	require.Equal(t, 15*time.Second, cfg.Server.ShutdownTimeout)
	require.Equal(t, []string{"http://localhost:3000"}, cfg.Server.CORSOrigins)

	require.Equal(t, testDatabaseURL, cfg.Database.URL)
	require.Equal(t, 15*time.Second, cfg.Database.ConnectTimeout)
	require.Equal(t, 10, cfg.Database.MaxConns)
	require.Equal(t, 2, cfg.Database.MinConns)

	require.Equal(t, testRedisURL, cfg.Redis.URL)
	require.Equal(t, 10*time.Second, cfg.Redis.ConnectTimeout)

	require.Equal(t, 15*time.Minute, cfg.Auth.AccessTokenTTL)
	require.Equal(t, 720*time.Hour, cfg.Auth.RefreshTokenTTL)

	// The mock provider is the default so the stack runs without an API key.
	require.Equal(t, config.ProviderMock, cfg.AI.Provider)
	require.Equal(t, 1536, cfg.AI.EmbeddingDimensions)
	require.Equal(t, 60*time.Second, cfg.AI.RequestTimeout)
}

func TestLoadReadsOverrides(t *testing.T) {
	isolateEnv(t)
	setRequired(t)
	t.Setenv("FLOWCAST_ENV", "production")
	t.Setenv("FLOWCAST_HTTP_PORT", "9999")
	t.Setenv("FLOWCAST_LOG_LEVEL", "debug")
	t.Setenv("FLOWCAST_ACCESS_TOKEN_TTL", "5m")
	t.Setenv("FLOWCAST_CORS_ORIGINS", "https://a.example , ,https://b.example")
	t.Setenv("FLOWCAST_DATABASE_MAX_CONNS", "25")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, config.EnvProduction, cfg.Env)
	require.True(t, cfg.Env.IsProduction())
	require.Equal(t, 9999, cfg.Server.Port)
	require.Equal(t, slog.LevelDebug, cfg.Server.LogLevel)
	require.Equal(t, 5*time.Minute, cfg.Auth.AccessTokenTTL)
	require.Equal(t, 25, cfg.Database.MaxConns)
	// Blank entries in the list are dropped.
	require.Equal(t, []string{"https://a.example", "https://b.example"}, cfg.Server.CORSOrigins)
}

func TestLoadReportsEveryMissingRequiredVariable(t *testing.T) {
	isolateEnv(t)

	vars := requireInvalid(t)

	// All three are reported together rather than failing on the first.
	require.Equal(t, []string{
		"FLOWCAST_DATABASE_URL",
		"FLOWCAST_JWT_SECRET",
		"FLOWCAST_REDIS_URL",
	}, vars)
}

func TestLoadTreatsBlankAsMissing(t *testing.T) {
	isolateEnv(t)
	setRequired(t)
	t.Setenv("FLOWCAST_JWT_SECRET", "   ")

	require.Contains(t, requireInvalid(t), "FLOWCAST_JWT_SECRET")
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantVar  string
		wantText string
	}{
		{
			name:    "unknown environment",
			env:     map[string]string{"FLOWCAST_ENV": "staging"},
			wantVar: "FLOWCAST_ENV",
		},
		{
			name:    "port not a number",
			env:     map[string]string{"FLOWCAST_HTTP_PORT": "http"},
			wantVar: "FLOWCAST_HTTP_PORT",
		},
		{
			name:    "port out of range",
			env:     map[string]string{"FLOWCAST_HTTP_PORT": "70000"},
			wantVar: "FLOWCAST_HTTP_PORT",
		},
		{
			name:    "database url wrong scheme",
			env:     map[string]string{"FLOWCAST_DATABASE_URL": "mysql://localhost:3306/flowcast"},
			wantVar: "FLOWCAST_DATABASE_URL",
		},
		{
			name:    "redis url wrong scheme",
			env:     map[string]string{"FLOWCAST_REDIS_URL": "http://localhost:6379"},
			wantVar: "FLOWCAST_REDIS_URL",
		},
		{
			name:     "jwt secret too short",
			env:      map[string]string{"FLOWCAST_JWT_SECRET": "tooshort"},
			wantVar:  "FLOWCAST_JWT_SECRET",
			wantText: "at least 32 characters",
		},
		{
			name:    "unparseable duration",
			env:     map[string]string{"FLOWCAST_ACCESS_TOKEN_TTL": "fifteen minutes"},
			wantVar: "FLOWCAST_ACCESS_TOKEN_TTL",
		},
		{
			name:     "access token outlives refresh token",
			env:      map[string]string{"FLOWCAST_ACCESS_TOKEN_TTL": "1000h"},
			wantVar:  "FLOWCAST_ACCESS_TOKEN_TTL",
			wantText: "shorter than FLOWCAST_REFRESH_TOKEN_TTL",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"FLOWCAST_LOG_LEVEL": "verbose"},
			wantVar: "FLOWCAST_LOG_LEVEL",
		},
		{
			name:    "unknown ai provider",
			env:     map[string]string{"FLOWCAST_AI_PROVIDER": "anthropic-but-unwired"},
			wantVar: "FLOWCAST_AI_PROVIDER",
		},
		{
			name: "min conns exceeds max conns",
			env: map[string]string{
				"FLOWCAST_DATABASE_MAX_CONNS": "4",
				"FLOWCAST_DATABASE_MIN_CONNS": "9",
			},
			wantVar:  "FLOWCAST_DATABASE_MIN_CONNS",
			wantText: "must not exceed",
		},
		{
			name:    "zero embedding dimensions",
			env:     map[string]string{"FLOWCAST_EMBEDDING_DIMENSIONS": "0"},
			wantVar: "FLOWCAST_EMBEDDING_DIMENSIONS",
		},
		{
			name:     "max conns above ceiling",
			env:      map[string]string{"FLOWCAST_DATABASE_MAX_CONNS": "5000"},
			wantVar:  "FLOWCAST_DATABASE_MAX_CONNS",
			wantText: "between 1 and 1000",
		},
		{
			name:    "non-positive connect timeout",
			env:     map[string]string{"FLOWCAST_DATABASE_CONNECT_TIMEOUT": "0s"},
			wantVar: "FLOWCAST_DATABASE_CONNECT_TIMEOUT",
		},
		{
			name:    "non-positive redis connect timeout",
			env:     map[string]string{"FLOWCAST_REDIS_CONNECT_TIMEOUT": "0s"},
			wantVar: "FLOWCAST_REDIS_CONNECT_TIMEOUT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateEnv(t)
			setRequired(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			cfg, err := config.Load()
			require.Error(t, err)
			require.Nil(t, cfg)

			var verr *config.ValidationError
			require.ErrorAs(t, err, &verr)
			require.Contains(t, verr.Variables(), tc.wantVar)
			if tc.wantText != "" {
				require.Contains(t, verr.Error(), tc.wantText)
			}
		})
	}
}

func TestOpenAIProviderRequiresCredentialsAndModels(t *testing.T) {
	isolateEnv(t)
	setRequired(t)
	t.Setenv("FLOWCAST_AI_PROVIDER", "openai")

	require.Equal(t, []string{
		"FLOWCAST_ANALYSIS_MODEL",
		"FLOWCAST_EMBEDDING_MODEL",
		"FLOWCAST_POSTMORTEM_MODEL",
		"OPENAI_API_KEY",
	}, requireInvalid(t))
}

func TestOpenAIProviderAcceptsCompleteConfiguration(t *testing.T) {
	isolateEnv(t)
	setRequired(t)
	t.Setenv("FLOWCAST_AI_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-test-not-a-real-key")
	t.Setenv("FLOWCAST_ANALYSIS_MODEL", "analysis-model")
	t.Setenv("FLOWCAST_POSTMORTEM_MODEL", "postmortem-model")
	t.Setenv("FLOWCAST_EMBEDDING_MODEL", "embedding-model")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, config.ProviderOpenAI, cfg.AI.Provider)
	require.Equal(t, "analysis-model", cfg.AI.AnalysisModel)
}

func TestValidationErrorMessageNamesEveryProblem(t *testing.T) {
	isolateEnv(t)

	_, err := config.Load()
	require.Error(t, err)

	msg := err.Error()
	require.Contains(t, msg, "3 problem(s)")
	for _, v := range []string{
		"FLOWCAST_DATABASE_URL", "FLOWCAST_REDIS_URL", "FLOWCAST_JWT_SECRET",
	} {
		require.Contains(t, msg, v)
	}
}

// TestLoggingRedactsSecrets is the important one: it renders the whole config through a
// real slog handler and asserts no secret appears in the output.
func TestLoggingRedactsSecrets(t *testing.T) {
	isolateEnv(t)
	setRequired(t)
	t.Setenv("FLOWCAST_AI_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "sk-super-secret-api-key")
	t.Setenv("FLOWCAST_ANALYSIS_MODEL", "analysis-model")
	t.Setenv("FLOWCAST_POSTMORTEM_MODEL", "postmortem-model")
	t.Setenv("FLOWCAST_EMBEDDING_MODEL", "embedding-model")

	cfg, err := config.Load()
	require.NoError(t, err)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("configuration", "config", cfg)
	out := buf.String()

	for _, secret := range []string{
		"sk-super-secret-api-key", // OpenAI key
		testJWTSecret,             // JWT signing key
		"secretpw",                // database password inside the DSN
	} {
		require.NotContains(t, out, secret, "secret leaked into logs")
	}

	// Non-sensitive fields still make it through, so the log stays useful.
	require.Contains(t, out, "[set]")
	require.Contains(t, out, "analysis-model")
	require.Contains(t, out, "localhost:5432")
	require.Contains(t, out, "xxxxx") // url.Redacted placeholder
}

func TestLoggingReportsUnsetSecrets(t *testing.T) {
	isolateEnv(t)
	setRequired(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("configuration", "config", cfg)
	require.Contains(t, buf.String(), "[unset]")
}
