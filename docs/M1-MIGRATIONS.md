# M1 — Durable schema migrations

## Why

The match service applies its schema idempotently on connect: `openPostgres`
runs `const pgSchema` (a pair of `CREATE TABLE IF NOT EXISTS` statements) every
time it starts. That is convenient for a fresh database and correct for the M1
baseline, but it offers **no way to evolve the schema**. Adding a column, an
index or a backfill through it would mean every deployment silently guessing
what the schema should look like, with no record of what has run where.

`server/cmd/migrate` adds the missing piece: an ordered, recorded migration
history under `infra/migrations/`.

## Layout

```text
infra/migrations/
└── 001_init.sql        # baseline: players, match_results
server/cmd/migrate/     # the runner
```

## Rules

- Filenames are `NNN_snake_case.sql` with a zero-padded version. Versions must
  start at 1 and have **no gaps** (enforced by
  `TestRepositoryMigrationsAreContiguousAndNamed`).
- A migration runs at most once, inside a single transaction, and is recorded in
  `schema_migrations(version, name, applied_at)` by the same transaction. A
  half-applied migration can therefore never be recorded as done.
- **Never edit an applied migration.** Add the next version.
- Prefer idempotent statements (`CREATE TABLE IF NOT EXISTS`,
  `CREATE INDEX IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS` via a `DO` block) so
  a re-run after a partial failure is safe.

## Usage

```bash
# show what would run, change nothing
go run ./cmd/migrate -dsn "$WORDARENA_POSTGRES_DSN" -plan

# apply
go run ./cmd/migrate -dsn "$WORDARENA_POSTGRES_DSN"
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-dsn` | `$WORDARENA_POSTGRES_DSN` | target database |
| `-dir` | resolved repository `infra/migrations` | migration directory |
| `-plan` | off | print pending migrations and exit |
| `-timeout` | `30s` | per-migration timeout; the overall budget scales with the number of migrations |

The runner is safe to run repeatedly: applied versions are skipped. With
`-plan` and no DSN it lists what is on disk without connecting.

## Drift guard

Two places can create the schema: the service on connect, and the migrations on
deploy. `TestBaselineMigrationMatchesServiceSchema` parses both — the
`pgSchema` literal in `server/cmd/game/store_pg.go` and
`infra/migrations/001_init.sql` — and fails the build if their tables or
columns differ. This is verified by mutation: adding a column to only one side
makes the suite fail.

This means a schema change must be made in **both** places, or better, made as
a new numbered migration and then reflected in `pgSchema` so a fresh
development database still comes up without running the runner.

## Operational guidance

- Run `migrate -plan` in the deploy pipeline and review the output before
  applying; it is cheap and read-only apart from creating the bookkeeping table.
- Run the runner **before** rolling out a new service build that expects new
  columns.
- Backups before destructive migrations are mandatory in production even though
  the runner is transactional: `TRUNCATE`, `DROP` and type changes are not
  protected by a transaction against human error.
- The tracked `schema_migrations` table is the source of truth for what a given
  environment has applied. Do not edit it by hand.
