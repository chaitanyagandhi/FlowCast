package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// dotEnvSearchDepth is how many parent directories are searched for a .env file. The
// backend module lives one level below the repository root, and tests run from deeper
// still, so a few levels of headroom keeps `go run ./cmd/server` working from anywhere in
// the tree.
const dotEnvSearchDepth = 5

// ErrNoDotEnv is returned when no .env file was found. It is not a failure: real
// environments supply configuration through the environment itself.
var ErrNoDotEnv = errors.New("no .env file found")

// LoadDotEnv finds the nearest .env file by walking up from the working directory and
// loads it into the environment, returning the path it used.
//
// Variables already present in the environment win: a .env file supplies local defaults,
// it does not override what the operator or container runtime set.
func LoadDotEnv() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determining working directory: %w", err)
	}
	return LoadDotEnvFrom(dir)
}

// LoadDotEnvFrom is LoadDotEnv rooted at an explicit directory.
func LoadDotEnvFrom(dir string) (string, error) {
	path, err := findDotEnv(dir)
	if err != nil {
		return "", err
	}
	if err := godotenv.Load(path); err != nil {
		return "", fmt.Errorf("loading %s: %w", path, err)
	}
	return path, nil
}

// findDotEnv walks up from dir looking for a readable .env file.
func findDotEnv(dir string) (string, error) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", dir, err)
	}

	for i := 0; i <= dotEnvSearchDepth; i++ {
		candidate := filepath.Join(current, ".env")
		info, statErr := os.Stat(candidate)
		switch {
		case statErr == nil && info.Mode().IsRegular():
			return candidate, nil
		case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
			return "", fmt.Errorf("checking %s: %w", candidate, statErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			break // reached the filesystem root
		}
		current = parent
	}

	return "", ErrNoDotEnv
}
