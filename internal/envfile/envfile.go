// Package envfile provides a minimal .env file loader for demos and tests.
package envfile

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Load reads a .env style file and returns its key/value pairs.
// Lines starting with '#' and blank lines are ignored. Values may be
// optionally wrapped in single or double quotes.
func Load(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	vars := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			vars[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

// Find searches for the named file in dir and its parents, returning the
// first match. It returns an empty string when not found.
func Find(dir, name string) string {
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// LoadNearest finds and loads the named env file starting from dir upward.
// It returns an empty map when no file is found.
func LoadNearest(dir, name string) (map[string]string, error) {
	path := Find(dir, name)
	if path == "" {
		return map[string]string{}, nil
	}
	return Load(path)
}
