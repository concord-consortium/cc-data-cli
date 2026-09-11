// Package duck opens an ephemeral in-memory DuckDB, registers a dataset's views
// from the manifest's explicit file lists, then locks the sandbox.
package duck

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
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
	warn io.Writer
	// pinned holds the views registered from a Parquet, so a later query can
	// notice the copy going stale and put the view back on its raw artifacts.
	pinned []pinnedView
}

// pinnedView is a view reading its Parquet, with the directory its freshness
// fingerprints resolve against.
type pinnedView struct {
	stmt viewStmt
	dir  string
}

// Open registers the datasets' views and locks the sandbox to their folders plus
// any allow-dirs. Warnings from degraded views are written to warnOut.
func Open(ctx context.Context, datasets []DatasetSpec, allowDirs []string, warnOut io.Writer) (*Engine, error) {
	e, _, err := open(ctx, datasets, allowDirs, warnOut, true)
	return e, err
}

// OpenRaw registers every view from its raw artifacts, ignoring any Parquet the
// manifest records, and reports the views that fell back to their typed-empty
// statement. Materialization reads through it so a stale Parquet can never be
// copied forward into a fresh-looking one.
func OpenRaw(ctx context.Context, d *dataset.Dataset, warnOut io.Writer) (*Engine, map[string]bool, error) {
	return open(ctx, []DatasetSpec{{DS: d}}, nil, warnOut, false)
}

func open(ctx context.Context, datasets []DatasetSpec, allowDirs []string, warnOut io.Writer, useMaterialized bool) (*Engine, map[string]bool, error) {
	degraded := map[string]bool{}
	if warnOut == nil {
		warnOut = io.Discard
	}
	if len(datasets) == 0 {
		return nil, nil, fmt.Errorf("no datasets given")
	}
	schemas, err := resolveSchemas(datasets)
	if err != nil {
		return nil, nil, err
	}

	var allowed []string
	for _, ds := range datasets {
		canon, err := canonicalize(ds.DS.Dir)
		if err != nil {
			return nil, nil, err
		}
		allowed = append(allowed, canon)
	}
	for _, dir := range allowDirs {
		canon, err := canonicalize(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("--allow-dir %q: %w", dir, err)
		}
		allowed = append(allowed, canon)
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	e := &Engine{db: db, conn: conn, warn: warnOut}

	multi := len(datasets) > 1
	for i, ds := range datasets {
		prefix := ""
		if multi {
			prefix = sqlIdent(schemas[i]) + "."
			if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+sqlIdent(schemas[i])); err != nil {
				e.Close()
				return nil, nil, fmt.Errorf("creating schema %q: %w", schemas[i], err)
			}
		}
		m, err := ds.DS.ReadManifest()
		if err != nil {
			e.Close()
			return nil, nil, err
		}
		canon, _ := canonicalize(ds.DS.Dir)
		vs := viewSet{prefix: prefix, canonDir: canon, m: m, warn: warnOut}
		stmts := vs.statements()
		if useMaterialized {
			stmts = vs.applyMaterialized(stmts)
		}
		for _, stmt := range stmts {
			pinned, deg, err := e.register(ctx, stmt)
			if err != nil {
				e.Close()
				return nil, nil, err
			}
			if pinned {
				e.pinned = append(e.pinned, pinnedView{stmt: stmt, dir: canon})
			}
			if deg {
				degraded[bareViewName(stmt.name, prefix)] = true
			}
		}
	}

	if err := e.lockSandbox(ctx, allowed); err != nil {
		e.Close()
		return nil, nil, err
	}
	return e, degraded, nil
}

// register creates one view, trying its forms in order: the Parquet when one is
// set, then the raw artifacts, then the typed-empty fallback. It reports whether
// the view ended pinned to its Parquet and whether it degraded to empty. A
// missing Parquet gets one short line; only a present but unreadable one
// carries the engine's error, since that is the case with something to explain.
func (e *Engine) register(ctx context.Context, st viewStmt) (pinned, degraded bool, err error) {
	if st.materialized != "" {
		if _, serr := os.Stat(st.parquet); serr != nil {
			fmt.Fprintf(e.warn, "warning: materialized %s is missing; reading the raw artifacts instead\n", st.name)
		} else if merr := e.exec(ctx, st.materialized); merr == nil {
			return true, false, nil
		} else {
			fmt.Fprintf(e.warn, "warning: materialized %s is unreadable (%v); reading the raw artifacts instead\n", st.name, merr)
		}
	}
	if perr := e.exec(ctx, st.primary); perr != nil {
		if ferr := e.exec(ctx, st.fallback); ferr != nil {
			return false, false, fmt.Errorf("registering view %s: %v (fallback also failed: %v)", st.name, perr, ferr)
		}
		fmt.Fprintf(e.warn, "warning: view %s degraded to empty (%v); affected files: %s\n", st.name, perr, strings.Join(st.files, ", "))
		return false, true, nil
	}
	return false, false, nil
}

// refreshPinned re-registers every pinned view whose Parquet has gone stale
// from its raw artifacts. The raw statements read their files on every query,
// so without this a session that outlives a fetch would keep answering from a
// copy of the data as it stood when the session opened.
func (e *Engine) refreshPinned(ctx context.Context) error {
	if len(e.pinned) == 0 {
		return nil
	}
	var kept []pinnedView
	for i, p := range e.pinned {
		if p.stmt.record.Fresh(p.dir, p.stmt.files, p.stmt.signature) {
			kept = append(kept, p)
			continue
		}
		raw := p.stmt
		raw.materialized = ""
		err := e.exec(ctx, "DROP VIEW "+raw.name)
		if err == nil {
			_, _, err = e.register(ctx, raw)
		}
		if err != nil {
			e.pinned = append(kept, e.pinned[i+1:]...)
			return err
		}
	}
	e.pinned = kept
	return nil
}

// Resource caps applied before lock_configuration=true. DuckDB otherwise
// defaults to roughly 80% of host RAM, every core, and an unbounded temp
// directory, so untrusted caller SQL (including MCP/LLM-supplied queries) could
// exhaust the machine. These bound a local denial of service; they are set once
// and then frozen by lock_configuration.
const (
	sandboxMemoryLimit = "2GB"
	sandboxTempDirSize = "4GB"
	sandboxThreads     = 4
)

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
		fmt.Sprintf("SET memory_limit = '%s'", sandboxMemoryLimit),
		fmt.Sprintf("SET max_temp_directory_size = '%s'", sandboxTempDirSize),
		fmt.Sprintf("SET threads = %d", sandboxThreads),
		"SET lock_configuration = true",
	}
	for _, s := range stmts {
		if _, err := e.conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("sandbox setup (%s): %w", s, err)
		}
	}
	return nil
}

// exec runs a statement that returns no rows. Query cannot serve here: dropping
// its *sql.Rows leaks the connection and deadlocks Close.
func (e *Engine) exec(ctx context.Context, stmt string) error {
	_, err := e.conn.ExecContext(ctx, stmt)
	return err
}

// Query runs user SQL on the pinned connection, after putting any view whose
// Parquet has gone stale back on its raw artifacts.
func (e *Engine) Query(ctx context.Context, query string) (*sql.Rows, error) {
	if err := e.refreshPinned(ctx); err != nil {
		return nil, err
	}
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
		// DuckDB schema names are ASCII case-insensitive, so collision and
		// reserved-name checks key on the lowercased name; School and school
		// would otherwise pass here and then collide inside DuckDB.
		key := strings.ToLower(name)
		if multi && ds.Alias == "" && reservedSchemaNames[key] {
			return nil, fmt.Errorf("dataset %s uses the reserved DuckDB schema name %q; give it an alias with pre=<alias>=%s",
				ds.DS.Ref, name, ds.DS.Ref)
		}
		if prev, ok := seen[key]; ok {
			return nil, fmt.Errorf("dataset schema name %q collides (datasets %s and %s); disambiguate with pre=<ref>",
				name, datasets[prev].DS.Ref, ds.DS.Ref)
		}
		seen[key] = i
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
