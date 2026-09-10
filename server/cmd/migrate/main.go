// Command migrate applies the versioned SQL migrations in infra/migrations to
// the durable PostgreSQL store used by the match service
// (WORDARENA_POSTGRES_DSN, see docs/M1-PERSISTENCE.md).
//
// Why this exists: the service applies its schema idempotently on connect
// (CREATE TABLE IF NOT EXISTS), which is convenient for a fresh database but
// offers no way to evolve it. Adding a column, an index or a backfill needs a
// recorded, ordered history — otherwise every deployment guesses what the
// schema should look like.
//
// Usage:
//
//	go run ./cmd/migrate -dsn "$WORDARENA_POSTGRES_DSN"
//	go run ./cmd/migrate -dsn "$DSN" -dir ../../infra/migrations -plan
//
// Flags:
//
//	-dsn      Postgres DSN; defaults to WORDARENA_POSTGRES_DSN
//	-dir      migration directory (default: resolved repository infra/migrations)
//	-plan     print the pending migrations and exit without changing anything
//	-timeout  per-migration statement timeout (default 30s)
//
// The command is safe to run repeatedly: already-applied versions are skipped.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// migration is one versioned SQL file.
type migration struct {
	Version int
	Name    string
	Path    string
	SQL     string
}

// versionRe matches NNN_name.sql. The version must be digits so ordering is
// numeric rather than lexical.
var versionRe = regexp.MustCompile(`^(\d+)_([A-Za-z0-9_-]+)\.sql$`)

// discover reads and validates the migrations in dir. It fails on a malformed
// filename, a duplicate version or an empty file, because any of those makes
// the applied history ambiguous across machines.
func discover(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read dir: %w", err)
	}
	var out []migration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := versionRe.FindStringSubmatch(e.Name())
		if m == nil {
			// Ignore non-migration files so README/notes can live alongside.
			if strings.HasSuffix(e.Name(), ".sql") {
				return nil, fmt.Errorf("migrate: %s does not match NNN_name.sql", e.Name())
			}
			continue
		}
		version, err := strconv.Atoi(m[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migrate: %s has an invalid version", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrate: version %d used by both %s and %s", version, prev, e.Name())
		}
		seen[version] = e.Name()

		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("migrate: %s is empty", e.Name())
		}
		out = append(out, migration{
			Version: version,
			Name:    m[2],
			Path:    path,
			SQL:     string(body),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("migrate: no migrations found in %s", dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// pending returns the migrations that have not been applied yet, in order.
func pending(all []migration, applied map[int]bool) []migration {
	var out []migration
	for _, m := range all {
		if !applied[m.Version] {
			out = append(out, m)
		}
	}
	return out
}

const migrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    BIGINT      PRIMARY KEY,
    name       TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// ensureTable creates the bookkeeping table. It is separate from the
// versioned migrations so the runner itself can evolve independently.
func ensureTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, migrationsTable); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	return nil
}

// appliedVersions reads the recorded history.
func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read history: %w", err)
	}
	defer rows.Close()
	out := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scan history: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

// apply runs one migration and records it in the same transaction, so a
// half-applied migration can never be recorded as done.
func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin %s: %w", m.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("migrate: exec %03d_%s: %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)
		 ON CONFLICT (version) DO NOTHING`, m.Version, m.Name); err != nil {
		return fmt.Errorf("migrate: record %03d_%s: %w", m.Version, m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit %03d_%s: %w", m.Version, m.Name, err)
	}
	return nil
}

// findMigrationsDir resolves the migration directory: an explicit -dir wins,
// otherwise walk up from the working directory looking for infra/migrations.
func findMigrationsDir(explicit string) (string, error) {
	if explicit != "" {
		info, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("migrate: -dir %s: %w", explicit, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("migrate: -dir %s is not a directory", explicit)
		}
		return explicit, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "infra", "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("migrate: could not locate infra/migrations from %s; pass -dir", wd)
}

func main() {
	dsnFlag := flag.String("dsn", os.Getenv("WORDARENA_POSTGRES_DSN"), "PostgreSQL DSN (default $WORDARENA_POSTGRES_DSN)")
	dirFlag := flag.String("dir", "", "migration directory (default: repository infra/migrations)")
	planFlag := flag.Bool("plan", false, "print pending migrations and exit without applying them")
	timeoutFlag := flag.Duration("timeout", 30*time.Second, "per-migration timeout")
	flag.Parse()

	if err := run(*dsnFlag, *dirFlag, *planFlag, *timeoutFlag); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(dsn, dirFlag string, planOnly bool, timeout time.Duration) error {
	dir, err := findMigrationsDir(dirFlag)
	if err != nil {
		return err
	}
	migrations, err := discover(dir)
	if err != nil {
		return err
	}

	if planOnly {
		// Without a DSN we can only report what exists on disk.
		if dsn == "" {
			fmt.Printf("migrations in %s (%d), no DSN given so applied state is unknown:\n", dir, len(migrations))
			for _, m := range migrations {
				fmt.Printf("  %03d_%s.sql\n", m.Version, m.Name)
			}
			return nil
		}
		db, err := openDB(dsn)
		if err != nil {
			return err
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := ensureTable(ctx, db); err != nil {
			return err
		}
		applied, err := appliedVersions(ctx, db)
		if err != nil {
			return err
		}
		list := pending(migrations, applied)
		if len(list) == 0 {
			fmt.Println("schema up to date; no pending migrations")
			return nil
		}
		fmt.Printf("%d pending migration(s):\n", len(list))
		for _, m := range list {
			fmt.Printf("  %03d_%s.sql\n", m.Version, m.Name)
		}
		return nil
	}

	if dsn == "" {
		return errors.New("no DSN: pass -dsn or set WORDARENA_POSTGRES_DSN")
	}
	db, err := openDB(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout*time.Duration(len(migrations)+2))
	defer cancel()
	if err := ensureTable(ctx, db); err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range pending(migrations, applied) {
		fmt.Printf("applying %03d_%s.sql ... ", m.Version, m.Name)
		if err := apply(ctx, db, m); err != nil {
			fmt.Println("FAILED")
			return err
		}
		fmt.Println("ok")
	}
	fmt.Println("schema up to date")
	return nil
}

func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
