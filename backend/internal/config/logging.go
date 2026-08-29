package config

import (
	"log/slog"
	"net/url"
)

// Redaction placeholders. Whether a secret is set is useful when debugging startup;
// its value never is.
const (
	secretSet     = "[set]"
	secretUnset   = "[unset]"
	unparseableID = "[unparseable]"
)

// Config, and each of its sections, implements slog.LogValuer so that logging the
// configuration cannot leak the JWT signing key, the OpenAI API key, or the database
// password. There is no code path that formats these fields verbatim.
var (
	_ slog.LogValuer = Config{}
	_ slog.LogValuer = DatabaseConfig{}
	_ slog.LogValuer = AuthConfig{}
	_ slog.LogValuer = AIConfig{}
)

// LogValue renders the whole configuration with every secret redacted.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", string(c.Env)),
		slog.Any("server", c.Server),
		slog.Any("database", c.Database),
		slog.Any("redis", c.Redis),
		slog.Any("auth", c.Auth),
		slog.Any("ai", c.AI),
	)
}

// LogValue reports the database URL with any password stripped.
func (d DatabaseConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", redactURL(d.URL)),
		slog.Int("max_conns", d.MaxConns),
		slog.Int("min_conns", d.MinConns),
		slog.Duration("max_conn_lifetime", d.MaxConnLifetime),
		slog.Duration("max_conn_idle_time", d.MaxConnIdleTime),
	)
}

// LogValue reports the Redis URL with any password stripped.
func (r RedisConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", redactURL(r.URL)),
		slog.Duration("connect_timeout", r.ConnectTimeout),
	)
}

// LogValue reports token lifetimes but never the signing key.
func (a AuthConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("jwt_secret", secretState(a.JWTSecret)),
		slog.Duration("access_token_ttl", a.AccessTokenTTL),
		slog.Duration("refresh_token_ttl", a.RefreshTokenTTL),
		slog.Int("bcrypt_cost", a.BcryptCost),
	)
}

// LogValue reports model selection -- which is useful for correlating results with
// experiments -- but never the API key.
func (a AIConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("provider", string(a.Provider)),
		slog.String("openai_api_key", secretState(a.OpenAIAPIKey)),
		slog.String("analysis_model", a.AnalysisModel),
		slog.String("postmortem_model", a.PostmortemModel),
		slog.String("embedding_model", a.EmbeddingModel),
		slog.Int("embedding_dimensions", a.EmbeddingDimensions),
		slog.Duration("request_timeout", a.RequestTimeout),
	)
}

// secretState says whether a secret was supplied, without revealing it or its length.
func secretState(s string) string {
	if s == "" {
		return secretUnset
	}
	return secretSet
}

// redactURL replaces the password in a connection string with a placeholder. A URL that
// does not parse is reported as unparseable rather than echoed back, since it may still
// contain credentials.
func redactURL(raw string) string {
	if raw == "" {
		return secretUnset
	}
	u, err := url.Parse(raw)
	if err != nil {
		return unparseableID
	}
	return u.Redacted()
}
