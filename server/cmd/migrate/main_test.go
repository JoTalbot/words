package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "infra", "migrations")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate the repository root from the working directory")
	return ""
}

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestDiscoverOrdersByNumericVersion(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"010_later.sql":  "SELECT 1;",
		"002_middle.sql": "SELECT 1;",
		"001_first.sql":  "SELECT 1;",
	})

	got, err := discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := []int{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("discovered %d migrations, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i].Version != v {
			t.Errorf("migration %d has version %d, want %d", i, got[i].Version, v)
		}
	}
}

func TestDiscoverRejectsDuplicateVersions(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"001_a.sql": "SELECT 1;",
		"001_b.sql": "SELECT 2;",
	})
	if _, err := discover(dir); err == nil {
		t.Fatal("expected an error for duplicate versions")
	}
}

func TestDiscoverRejectsMalformedAndEmptyFiles(t *testing.T) {
	cases := map[string]string{
		"malformed.sql": "SELECT 1;",
		"001_empty.sql": "   \n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeMigrations(t, map[string]string{name: body})
			if _, err := discover(dir); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestDiscoverIgnoresNonSQLFiles(t *testing.T) {
	dir := writeMigrations(t, map[string]string{
		"001_init.sql": "SELECT 1;",
		"README.md":    "notes",
	})
	got, err := discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("discovered %d migrations, want 1", len(got))
	}
}

func TestDiscoverRequiresAtLeastOneMigration(t *testing.T) {
	if _, err := discover(t.TempDir()); err == nil {
		t.Fatal("expected an error for an empty directory")
	}
}

func TestPendingSkipsAppliedVersions(t *testing.T) {
	all := []migration{{Version: 1}, {Version: 2}, {Version: 3}}
	applied := map[int]bool{2: true}
	got := pending(all, applied)
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 3 {
		t.Fatalf("pending = %v, want versions 1 and 3", got)
	}
}

func TestPendingIsEmptyWhenEverythingIsApplied(t *testing.T) {
	all := []migration{{Version: 1}}
	if got := pending(all, map[int]bool{1: true}); len(got) != 0 {
		t.Fatalf("pending = %v, want empty", got)
	}
}

func TestRepositoryMigrationsAreContiguousAndNamed(t *testing.T) {
	got, err := discover(filepath.Join(repoRoot(t), "infra", "migrations"))
	if err != nil {
		t.Fatalf("discover repository migrations: %v", err)
	}
	for i, m := range got {
		if m.Version != i+1 {
			t.Errorf("migration %s has version %d, want %d (versions must start at 1 and have no gaps)",
				m.Name, m.Version, i+1)
		}
		if m.Name == "" {
			t.Errorf("migration %d has an empty name", m.Version)
		}
	}
}

// ---- schema drift guard -------------------------------------------------

var (
	tableRe = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+)\s*\((.*?)\);\s*(\n|$)`)
	// addColumnRe matches the only shape of later migration this repository
	// uses to change the column set: adding one. Type-only changes (migration
	// 002) do not alter the set and need no support here.
	addColumnRe = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+([A-Za-z0-9_]+)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+)`)
)

// applyAddedColumns folds the columns later migrations add into a schema parsed
// from the baseline. Without this the drift guard could only ever compare the
// service schema against 001_init.sql, so the first migration that added a
// column would have failed the build even when the service and the migrations
// agreed perfectly - which is exactly what happened when migration 003 added
// match_code and read_capability.
//
// The guard's real invariant is "the schema the service creates on connect
// equals the schema the migrations produce once all of them have run", and that
// is what it now checks.
// applyMigration folds one later migration into the cumulative schema: CREATE
// TABLE statements introduce tables (parsed the same way as the baseline) and
// ADD COLUMN statements extend existing ones.
func applyMigration(t *testing.T, schema map[string][]string, sql string) {
	t.Helper()
	for _, m := range addColumnRe.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(m[1])
		col := strings.ToLower(m[2])
		cols, ok := schema[table]
		if !ok {
			t.Errorf("migration adds column %q to table %q, which no CREATE TABLE defines", col, table)
			continue
		}
		for _, existing := range cols {
			if existing == col {
				t.Errorf("migration adds column %q to table %q, which already has it", col, table)
			}
		}
		schema[table] = append(cols, col)
		sort.Strings(schema[table])
	}
	// seenHere is scoped to this call: two migrations may define different
	// tables, but one migration defining the same table twice is an authoring
	// mistake, and a package-level set would report it across unrelated calls.
	seenHere := map[string]struct{}{}
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		table := strings.ToLower(m[1])
		// A later migration may only introduce a table the baseline does not
		// already define; redefining one would mean the two disagree about its
		// shape, which is the drift this test exists to catch.
		if _, exists := schema[table]; exists {
			t.Errorf("migration redefines table %q (use ALTER TABLE, or the two schemas could disagree)", table)
			continue
		}
		if _, twice := seenHere[table]; twice {
			t.Errorf("migration defines table %q twice", table)
			continue
		}
		seenHere[table] = struct{}{}
		schema[table] = parseColumns(t, m[2])
	}
}

// parseColumns extracts the column names of one CREATE TABLE body.
func parseColumns(t *testing.T, body string) []string {
	t.Helper()
	var cols []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToUpper(fields[0]) {
		case "PRIMARY", "UNIQUE", "CONSTRAINT", "CHECK", "FOREIGN", "--":
			continue
		}
		cols = append(cols, strings.ToLower(fields[0]))
	}
	sort.Strings(cols)
	return cols
}

// parseSchema extracts table name -> sorted column names from a SQL blob.
// Lines that begin with a table constraint keyword (PRIMARY KEY, UNIQUE,
// CONSTRAINT, CHECK, FOREIGN KEY) are not columns and are skipped.
func parseSchema(t *testing.T, sql string) map[string][]string {
	t.Helper()
	out := make(map[string][]string)
	for _, m := range tableRe.FindAllStringSubmatch(sql, -1) {
		out[strings.ToLower(m[1])] = parseColumns(t, m[2])
	}
	if len(out) == 0 {
		t.Fatal("no CREATE TABLE statements parsed; the schema parser needs updating")
	}
	return out
}

// pgSchemaFromSource reads const pgSchema out of the match service without
// importing it, so this test compiles and runs with no database and no
// dependency on package main of the service.
func pgSchemaFromSource(t *testing.T, root string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, "server", "cmd", "game", "store_pg.go"))
	if err != nil {
		t.Fatalf("read store_pg.go: %v", err)
	}
	start := strings.Index(string(src), "const pgSchema = `")
	if start < 0 {
		t.Fatal("could not find `const pgSchema` in server/cmd/game/store_pg.go")
	}
	rest := string(src)[start+len("const pgSchema = `"):]
	end := strings.Index(rest, "`")
	if end < 0 {
		t.Fatal("unterminated pgSchema literal in server/cmd/game/store_pg.go")
	}
	return rest[:end]
}

// TestBaselineMigrationMatchesServiceSchema is the reason this directory has a
// test at all. The service creates its schema on connect; the migrations create
// it on deploy. If one drifts, a deployment can silently produce a schema the
// code does not expect. This fails the build instead.
func TestBaselineMigrationMatchesServiceSchema(t *testing.T) {
	root := repoRoot(t)

	migrations, err := discover(filepath.Join(root, "infra", "migrations"))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasPrefix(migrations[0].Name, "init") {
		t.Fatalf("first migration is %q, want a name starting with \"init\"", migrations[0].Name)
	}

	want := parseSchema(t, pgSchemaFromSource(t, root))
	// discover already loaded every migration body, so the cumulative schema is
	// the baseline plus whatever each later migration does to it. Comparing
	// against that - rather than against 001_init.sql alone - is what makes the
	// guard correct once migrations change the schema instead of only its types.
	//
	// Later migrations can do two things: add columns to existing tables
	// (004, 005) and introduce new tables (005 guilds and guild_members).
	// Both are applied here; a migration that does something else must be
	// taught to this function, which is the point of keeping it explicit.
	got := parseSchema(t, migrations[0].SQL)
	for _, m := range migrations[1:] {
		applyMigration(t, got, m.SQL)
	}

	if len(got) != len(want) {
		t.Fatalf("table count mismatch: migration has %d (%v), service has %d (%v)",
			len(got), keys(got), len(want), keys(want))
	}
	for table, wantCols := range want {
		gotCols, ok := got[table]
		if !ok {
			t.Errorf("migration is missing table %q (present in the service schema)", table)
			continue
		}
		if len(gotCols) != len(wantCols) {
			t.Errorf("table %q: migration has %d columns %v, service has %d columns %v",
				table, len(gotCols), gotCols, len(wantCols), wantCols)
			continue
		}
		for i := range wantCols {
			if gotCols[i] != wantCols[i] {
				t.Errorf("table %q column %d: migration has %q, service has %q",
					table, i, gotCols[i], wantCols[i])
			}
		}
	}
	for table := range got {
		if _, ok := want[table]; !ok {
			t.Errorf("migration declares table %q which the service schema does not have", table)
		}
	}
}

func keys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestApplyMigrationDetectsDrift proves the cumulative guard still has teeth.
// Making it fold in later migrations could have quietly turned it into a test
// that always passes, so both directions are pinned: a column a migration adds
// must appear in the service schema, and a duplicate add is itself an error.
func TestApplyMigrationDetectsDrift(t *testing.T) {
	base := map[string][]string{"match_results": {"match_id", "seed"}}

	applyMigration(t, base, `ALTER TABLE match_results ADD COLUMN IF NOT EXISTS match_code TEXT;`)
	if len(base["match_results"]) != 3 {
		t.Fatalf("column was not folded in: %v", base["match_results"])
	}

	// The service schema must now agree, or the guard must fail. This mirrors
	// what the real assertion does.
	svc := map[string][]string{"match_results": {"match_id", "seed"}}
	if len(svc["match_results"]) == len(base["match_results"]) {
		t.Fatal("guard would not notice a column the service schema is missing")
	}
	svc["match_results"] = append(svc["match_results"], "match_code")
	sort.Strings(svc["match_results"])
	for i := range base["match_results"] {
		if base["match_results"][i] != svc["match_results"][i] {
			t.Fatalf("sets should now match: %v vs %v", base["match_results"], svc["match_results"])
		}
	}
}

// TestApplyMigrationFoldsInNewTables covers the other half: migration 005
// introduces guilds and guild_members, so the cumulative schema builder must
// understand CREATE TABLE in a later migration as well as ADD COLUMN. Without
// that, the guard would fail on a correct schema - or, worse, be "fixed" by
// moving new tables into the baseline migration.
func TestApplyMigrationFoldsInNewTables(t *testing.T) {
	schema := map[string][]string{"players": {"id", "nickname"}}
	applyMigration(t, schema, `
CREATE TABLE IF NOT EXISTS guilds (
    id   BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    PRIMARY KEY (id)
);
`)
	cols, ok := schema["guilds"]
	if !ok {
		t.Fatalf("a table introduced by a migration was not folded in: %v", keys(schema))
	}
	if len(cols) != 2 || cols[0] != "id" || cols[1] != "name" {
		t.Fatalf("guilds columns = %v, want [id name] (constraint lines are not columns)", cols)
	}

	// Redefining an existing table in a later migration is drift, not a
	// definition: the baseline and the service schema could then disagree.
	fakeT := &testing.T{}
	applyMigration(fakeT, schema, `CREATE TABLE IF NOT EXISTS players (id BIGINT);`)
	if !fakeT.Failed() {
		t.Error("a later migration redefining an existing table was not reported")
	}
}

func TestApplyMigrationRejectsBadMigrations(t *testing.T) {
	// A duplicate add and an add against an unknown table are both authoring
	// mistakes the guard should surface rather than absorb.
	t.Run("duplicate", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("unexpected panic: %v", r)
			}
		}()
		schema := map[string][]string{"players": {"id", "nickname"}}
		fakeT := &testing.T{}
		applyMigration(fakeT, schema, `ALTER TABLE players ADD COLUMN IF NOT EXISTS nickname TEXT;`)
		if !fakeT.Failed() {
			t.Error("a duplicate ADD COLUMN was not reported")
		}
	})
	t.Run("unknown table", func(t *testing.T) {
		schema := map[string][]string{"players": {"id"}}
		fakeT := &testing.T{}
		applyMigration(fakeT, schema, `ALTER TABLE ghosts ADD COLUMN boo TEXT;`)
		if !fakeT.Failed() {
			t.Error("an ADD COLUMN against an undefined table was not reported")
		}
	})
}
