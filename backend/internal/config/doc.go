// Package config loads and validates FlowCast configuration from the environment,
// failing fast at startup when a required variable is missing or malformed. It is the
// single place where model names, connection strings, and secrets enter the process.
package config
