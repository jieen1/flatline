// Package migrations embeds the SQL migration files and exposes them in
// version order. Files are named <version>_<name>.sql (e.g. 001_initial.sql).
package migrations

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var fs embed.FS

// Migration is a single versioned SQL migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

var nameRe = regexp.MustCompile(`^(\d+)_`)

// All returns every embedded migration sorted by ascending version.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("migrations: read embedded files: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := nameRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrations: %q does not match <version>_<name>.sql", e.Name())
		}
		version, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migrations: bad version in %q: %w", e.Name(), err)
		}
		sqlBytes, err := fs.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations: read %q: %w", e.Name(), err)
		}
		out = append(out, Migration{
			Version: version,
			Name:    strings.TrimSuffix(e.Name(), ".sql"),
			SQL:     string(sqlBytes),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("migrations: duplicate version %d (%q and %q)", out[i].Version, out[i-1].Name, out[i].Name)
		}
	}
	return out, nil
}
