package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// minJWTSecretLength is the shortest signing key we accept. HS256 keys shorter than the
// hash output add no security, so anything below this is a misconfiguration.
const minJWTSecretLength = 32

// maxDatabaseConns is an upper bound on the pool size. It is far above anything this
// system needs and exists to catch a fat-fingered value before pgx has to widen it to an
// int32.
const maxDatabaseConns = 1000

// Config is the fully resolved, validated configuration for the FlowCast backend. It is
// built once at startup and passed down by value or pointer; nothing reads the environment
// after Load returns.
type Config struct {
	Env      Environment
	Server   ServerConfig
	Database DatabaseConfig
	Redis    RedisConfig
	Auth     AuthConfig
	AI       AIConfig
}

// ServerConfig controls the HTTP listener and logging.
type ServerConfig struct {
	Port            int
	LogLevel        slog.Level
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	CORSOrigins     []string
}

// DatabaseConfig describes the PostgreSQL connection and pool sizing.
type DatabaseConfig struct {
	URL string
	// ConnectTimeout bounds the startup connectivity check, which retries while
	// PostgreSQL is still booting.
	ConnectTimeout  time.Duration
	MaxConns        int
	MinConns        int
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// RedisConfig describes the Redis connection used for queues and idempotency.
type RedisConfig struct {
	URL string
	// ConnectTimeout bounds the startup connectivity check, which retries while Redis
	// is still booting.
	ConnectTimeout time.Duration
}

// AuthConfig holds the JWT signing key and token lifetimes.
type AuthConfig struct {
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// AIConfig is the single place model names and AI credentials enter the process. No other
// package should hardcode a model identifier.
type AIConfig struct {
	Provider            AIProvider
	OpenAIAPIKey        string
	AnalysisModel       string
	PostmortemModel     string
	EmbeddingModel      string
	EmbeddingDimensions int
	RequestTimeout      time.Duration
}

// Environment names the deployment context.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvProduction  Environment = "production"
)

// IsProduction reports whether the process is running in production.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// AIProvider selects which Analyzer implementation is wired up at startup.
type AIProvider string

const (
	// ProviderOpenAI calls the OpenAI API and requires credentials and model names.
	ProviderOpenAI AIProvider = "openai"
	// ProviderMock returns canned analyses. It is the default so the stack runs, and
	// tests stay deterministic, without an API key or spending money.
	ProviderMock AIProvider = "mock"
)

// Load reads configuration from the process environment, applies defaults, and validates
// the result. It reports every problem it finds at once rather than failing on the first,
// so a misconfigured deployment can be fixed in a single pass.
//
// Load does not read files. Call LoadDotEnv first if .env support is wanted.
func Load() (*Config, error) {
	r := &reader{}

	cfg := &Config{
		Env: Environment(r.str("FLOWCAST_ENV", string(EnvDevelopment))),
		Server: ServerConfig{
			Port:            r.intVal("FLOWCAST_HTTP_PORT", 8080),
			LogLevel:        r.logLevel("FLOWCAST_LOG_LEVEL", slog.LevelInfo),
			ReadTimeout:     r.duration("FLOWCAST_HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    r.duration("FLOWCAST_HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     r.duration("FLOWCAST_HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: r.duration("FLOWCAST_SHUTDOWN_TIMEOUT", 15*time.Second),
			CORSOrigins:     r.strSlice("FLOWCAST_CORS_ORIGINS", []string{"http://localhost:3000"}),
		},
		Database: DatabaseConfig{
			URL:             r.requiredStr("FLOWCAST_DATABASE_URL"),
			ConnectTimeout:  r.duration("FLOWCAST_DATABASE_CONNECT_TIMEOUT", 15*time.Second),
			MaxConns:        r.intVal("FLOWCAST_DATABASE_MAX_CONNS", 10),
			MinConns:        r.intVal("FLOWCAST_DATABASE_MIN_CONNS", 2),
			MaxConnLifetime: r.duration("FLOWCAST_DATABASE_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: r.duration("FLOWCAST_DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		},
		Redis: RedisConfig{
			URL:            r.requiredStr("FLOWCAST_REDIS_URL"),
			ConnectTimeout: r.duration("FLOWCAST_REDIS_CONNECT_TIMEOUT", 10*time.Second),
		},
		Auth: AuthConfig{
			JWTSecret:       r.requiredStr("FLOWCAST_JWT_SECRET"),
			AccessTokenTTL:  r.duration("FLOWCAST_ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL: r.duration("FLOWCAST_REFRESH_TOKEN_TTL", 720*time.Hour),
		},
		AI: AIConfig{
			Provider:            AIProvider(r.str("FLOWCAST_AI_PROVIDER", string(ProviderMock))),
			OpenAIAPIKey:        r.str("OPENAI_API_KEY", ""),
			AnalysisModel:       r.str("FLOWCAST_ANALYSIS_MODEL", ""),
			PostmortemModel:     r.str("FLOWCAST_POSTMORTEM_MODEL", ""),
			EmbeddingModel:      r.str("FLOWCAST_EMBEDDING_MODEL", ""),
			EmbeddingDimensions: r.intVal("FLOWCAST_EMBEDDING_DIMENSIONS", 1536),
			RequestTimeout:      r.duration("FLOWCAST_AI_REQUEST_TIMEOUT", 60*time.Second),
		},
	}

	cfg.validate(r)

	if len(r.errs) > 0 {
		return nil, &ValidationError{Fields: r.errs}
	}
	return cfg, nil
}

// validate applies the checks that need a parsed value or span more than one variable.
func (c *Config) validate(r *reader) {
	switch c.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		r.add("FLOWCAST_ENV", fmt.Sprintf(
			"must be one of development, test, production; got %q", c.Env))
	}

	if c.Server.Port < 1 || c.Server.Port > 65535 {
		r.add("FLOWCAST_HTTP_PORT", fmt.Sprintf(
			"must be between 1 and 65535; got %d", c.Server.Port))
	}

	c.validateDatabase(r)
	c.validateRedis(r)
	c.validateAuth(r)
	c.validateAI(r)
}

func (c *Config) validateDatabase(r *reader) {
	if c.Database.URL != "" {
		if err := checkURLScheme(c.Database.URL, "postgres", "postgresql"); err != nil {
			r.add("FLOWCAST_DATABASE_URL", err.Error())
		}
	}
	if c.Database.ConnectTimeout <= 0 {
		r.add("FLOWCAST_DATABASE_CONNECT_TIMEOUT", "must be a positive duration")
	}
	if c.Database.MaxConns < 1 || c.Database.MaxConns > maxDatabaseConns {
		r.add("FLOWCAST_DATABASE_MAX_CONNS", fmt.Sprintf(
			"must be between 1 and %d; got %d", maxDatabaseConns, c.Database.MaxConns))
	}
	if c.Database.MinConns < 0 {
		r.add("FLOWCAST_DATABASE_MIN_CONNS", fmt.Sprintf(
			"must not be negative; got %d", c.Database.MinConns))
	}
	if c.Database.MinConns > c.Database.MaxConns {
		r.add("FLOWCAST_DATABASE_MIN_CONNS", fmt.Sprintf(
			"must not exceed FLOWCAST_DATABASE_MAX_CONNS (%d); got %d",
			c.Database.MaxConns, c.Database.MinConns))
	}
}

func (c *Config) validateRedis(r *reader) {
	if c.Redis.ConnectTimeout <= 0 {
		r.add("FLOWCAST_REDIS_CONNECT_TIMEOUT", "must be a positive duration")
	}
	if c.Redis.URL == "" {
		return
	}
	if err := checkURLScheme(c.Redis.URL, "redis", "rediss"); err != nil {
		r.add("FLOWCAST_REDIS_URL", err.Error())
	}
}

func (c *Config) validateAuth(r *reader) {
	if c.Auth.JWTSecret != "" && len(c.Auth.JWTSecret) < minJWTSecretLength {
		r.add("FLOWCAST_JWT_SECRET", fmt.Sprintf(
			"must be at least %d characters; got %d",
			minJWTSecretLength, len(c.Auth.JWTSecret)))
	}
	if c.Auth.AccessTokenTTL <= 0 {
		r.add("FLOWCAST_ACCESS_TOKEN_TTL", "must be a positive duration")
	}
	if c.Auth.RefreshTokenTTL <= 0 {
		r.add("FLOWCAST_REFRESH_TOKEN_TTL", "must be a positive duration")
	}
	if c.Auth.AccessTokenTTL > 0 && c.Auth.RefreshTokenTTL > 0 &&
		c.Auth.AccessTokenTTL >= c.Auth.RefreshTokenTTL {
		r.add("FLOWCAST_ACCESS_TOKEN_TTL",
			"must be shorter than FLOWCAST_REFRESH_TOKEN_TTL")
	}
}

func (c *Config) validateAI(r *reader) {
	switch c.AI.Provider {
	case ProviderMock:
		// The mock analyzer needs no credentials or model names.
	case ProviderOpenAI:
		if c.AI.OpenAIAPIKey == "" {
			r.add("OPENAI_API_KEY",
				"is required when FLOWCAST_AI_PROVIDER=openai")
		}
		for _, m := range []struct{ key, value string }{
			{"FLOWCAST_ANALYSIS_MODEL", c.AI.AnalysisModel},
			{"FLOWCAST_POSTMORTEM_MODEL", c.AI.PostmortemModel},
			{"FLOWCAST_EMBEDDING_MODEL", c.AI.EmbeddingModel},
		} {
			if m.value == "" {
				r.add(m.key, "is required when FLOWCAST_AI_PROVIDER=openai")
			}
		}
	default:
		r.add("FLOWCAST_AI_PROVIDER", fmt.Sprintf(
			"must be one of openai, mock; got %q", c.AI.Provider))
	}

	if c.AI.EmbeddingDimensions < 1 {
		r.add("FLOWCAST_EMBEDDING_DIMENSIONS", fmt.Sprintf(
			"must be at least 1; got %d", c.AI.EmbeddingDimensions))
	}
	if c.AI.RequestTimeout <= 0 {
		r.add("FLOWCAST_AI_REQUEST_TIMEOUT", "must be a positive duration")
	}
}

// checkURLScheme verifies that raw parses as a URL carrying one of the allowed schemes.
func checkURLScheme(raw string, allowed ...string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid URL: %v", err)
	}
	for _, s := range allowed {
		if u.Scheme == s {
			return nil
		}
	}
	return fmt.Errorf("scheme must be one of %s; got %q",
		strings.Join(allowed, ", "), u.Scheme)
}
