// Package duck opens an ephemeral in-memory DuckDB, registers a dataset's views
// from the manifest's explicit file lists, then locks the sandbox.
package duck

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	_ "github.com/duckdb/duckdb-go/v2"
)

// DatasetSpec names a dataset to register, with an optional schema alias.
type DatasetSpec struct {
	Alias string
	DS    *dataset.Dataset
}

// Engine is an open, sandboxed DuckDB session over one or more datasets.
type Engine struct {
	db   *sql.DB
	conn *sql.Conn
}

// Open registers the datasets' views and locks the sandbox to their folders plus
// any allow-dirs. Warnings from degraded views are written to warnOut.
func Open(ctx context.Context, datasets []DatasetSpec, allowDirs []string, warnOut io.Writer) (*Engine, error) {
	if len(datasets) == 0 {
		return nil, fmt.Errorf("no datasets given")
	}
	schemas, err := resolveSchemas(datasets)
	if err != nil {
		return nil, err
	}

	var allowed []string
	for _, ds := range datasets {
		canon, err := canonicalize(ds.DS.Dir)
		if err != nil {
			return nil, err
		}
		allowed = append(allowed, canon)
	}
	for _, dir := range allowDirs {
		canon, err := canonicalize(dir)
		if err != nil {
			return nil, fmt.Errorf("--allow-dir %q: %w", dir, err)
		}
		allowed = append(allowed, canon)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	e := &Engine{db: db, conn: conn}

	multi := len(datasets) > 1
	for i, ds := range datasets {
		prefix := ""
		if multi {
			prefix = sqlIdent(schemas[i]) + "."
			if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+sqlIdent(schemas[i])); err != nil {
				e.Close()
				return nil, fmt.Errorf("creating schema %q: %w", schemas[i], err)
			}
		}
		m, err := ds.DS.ReadManifest()
		if err != nil {
			e.Close()
			return nil, err
		}
		canon, _ := canonicalize(ds.DS.Dir)
		vs := viewSet{prefix: prefix, canonDir: canon, m: m, warn: warnOut}
		for _, stmt := range vs.statements() {
			if _, err := conn.ExecContext(ctx, stmt.primary); err != nil {
				if _, ferr := conn.ExecContext(ctx, stmt.fallback); ferr != nil {
					e.Close()
					return nil, fmt.Errorf("registering view %s: %v (fallback also failed: %v)", stmt.name, err, ferr)
				}
				fmt.Fprintf(warnOut, "warning: view %s degraded to empty (%v); affected files: %s\n", stmt.name, err, strings.Join(stmt.files, ", "))
			}
		}
	}

	if err := e.lockSandbox(ctx, allowed); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

func (e *Engine) lockSandbox(ctx context.Context, allowed []string) error {
	quoted := make([]string, len(allowed))
	for i, d := range allowed {
		quoted[i] = sqlStr(d)
	}
	stmts := []string{
		fmt.Sprintf("SET allowed_directories = [%s]", strings.Join(quoted, ", ")),
		"SET enable_external_access = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET allow_community_extensions = false",
		"SET lock_configuration = true",
	}
	for _, s := range stmts {
		if _, err := e.conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sandbox setup (%s): %w", s, err)
		}
	}
	return nil
}

// Query runs user SQL on the pinned connection.
func (e *Engine) Query(ctx context.Context, query string) (*sql.Rows, error) {
	return e.conn.QueryContext(ctx, query)
}

// Close releases the connection and database.
func (e *Engine) Close() error {
	if e.conn != nil {
		e.conn.Close()
	}
	if e.db != nil {
		return e.db.Close()
	}
	return nil
}

// reservedSchemaNames are DuckDB built-in schemas a multi-dataset registration
// cannot use as a dataset schema; a legacy dataset named one of these (created
// before the name validator excluded them) must supply a pre=<ref> alias.
var reservedSchemaNames = map[string]bool{
	"main": true, "temp": true, "system": true, "information_schema": true, "pg_catalog": true,
}

// resolveSchemas assigns a schema name per dataset (alias or dataset name),
// erroring on collisions and reserved names so they must be disambiguated with
// pre=<ref>. Only meaningful for multi-dataset registration.
func resolveSchemas(datasets []DatasetSpec) ([]string, error) {
	schemas := make([]string, len(datasets))
	seen := map[string]int{}
	multi := len(datasets) > 1
	for i, ds := range datasets {
		name := ds.Alias
		if name == "" {
			name = ds.DS.Ref.Name
		}
		if multi && ds.Alias == "" && reservedSchemaNames[name] {
			return nil, fmt.Errorf("dataset %s uses the reserved DuckDB schema name %q; give it an alias with pre=<alias>=%s",
				ds.DS.Ref, name, ds.DS.Ref)
		}
		if prev, ok := seen[name]; ok {
			return nil, fmt.Errorf("dataset schema name %q collides (datasets %s and %s); disambiguate with pre=<ref>",
				name, datasets[prev].DS.Ref, ds.DS.Ref)
		}
		seen[name] = i
		schemas[i] = name
	}
	return schemas, nil
}

// canonicalize resolves symlinks and returns an absolute path; it falls back to
// Abs when the path cannot be fully resolved.
func canonicalize(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Abs(path)
}
