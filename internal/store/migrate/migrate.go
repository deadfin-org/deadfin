// SPDX-License-Identifier: GPL-2.0-only

package migrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var embeddedMigrations embed.FS

// Dialect identifies the migration set and SQL grammar to use.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// UpOption configures one Up run.
type UpOption func(*upOptions)

type upOptions struct {
	advisoryLock   bool
	advisoryLockID int64
}

// WithAdvisoryLock makes Up hold the PostgreSQL session advisory lock id while
// it applies migrations, so concurrent starts against one database apply each
// migration once. The lock holds one connection of db for the whole run, so
// db must allow at least two open connections. Only PostgreSQL supports it.
func WithAdvisoryLock(id int64) UpOption {
	return func(options *upOptions) {
		options.advisoryLock = true
		options.advisoryLockID = id
	}
}

// Up applies all pending embedded migrations for dialect.
func Up(ctx context.Context, db *sql.DB, dialect Dialect, options ...UpOption) error {
	var config upOptions
	for _, option := range options {
		option(&config)
	}
	var extra []goose.ProviderOption
	if config.advisoryLock {
		if dialect != Postgres {
			return fmt.Errorf("advisory lock is not available for %s migrations", dialect)
		}
		if db != nil && db.Stats().MaxOpenConnections == 1 {
			return errors.New("advisory-locked migrations need a pool with at least two connections")
		}
		// Probe each second for up to five minutes, so a waiting start
		// follows soon after the holder finishes.
		locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(config.advisoryLockID), lock.WithLockTimeout(1, 300))
		if err != nil {
			return fmt.Errorf("create %s migration lock: %w", dialect, err)
		}
		extra = append(extra, goose.WithSessionLocker(locker))
	}
	provider, err := providerFor(db, dialect, extra...)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply %s migrations: %w", dialect, err)
	}
	return nil
}

// Down rolls back the most recently applied embedded migration for dialect.
func Down(ctx context.Context, db *sql.DB, dialect Dialect) error {
	provider, err := providerFor(db, dialect)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("roll back %s migration: %w", dialect, err)
	}
	return nil
}

// DownTo rolls back all applied migrations newer than version while preserving
// version itself as the current schema.
func DownTo(ctx context.Context, db *sql.DB, dialect Dialect, version int64) error {
	provider, err := providerFor(db, dialect)
	if err != nil {
		return err
	}
	if _, err := provider.DownTo(ctx, version); err != nil {
		return fmt.Errorf("roll back %s migrations to %05d: %w", dialect, version, err)
	}
	return nil
}

// ValidateTwins confirms that every embedded SQLite SQL migration has an
// identically named PostgreSQL migration and vice versa.
func ValidateTwins() error {
	return ValidateTwinsFS(embeddedMigrations)
}

// ValidateTwinsFS validates migration twins in fsys. It accepts the embedded
// migrations/ root and a direct sqlite/postgres root for focused tests.
func ValidateTwinsFS(fsys fs.FS) error {
	_, err := validateTwinsFS(fsys)
	return err
}

func providerFor(db *sql.DB, dialect Dialect, extra ...goose.ProviderOption) (*goose.Provider, error) {
	if db == nil {
		return nil, errors.New("migration database is required")
	}
	root, err := validateTwinsFS(embeddedMigrations)
	if err != nil {
		return nil, err
	}

	var gooseDialect goose.Dialect
	switch dialect {
	case SQLite:
		gooseDialect = goose.DialectSQLite3
	case Postgres:
		gooseDialect = goose.DialectPostgres
	default:
		return nil, fmt.Errorf("unsupported migration dialect %q", dialect)
	}

	migrations, err := fs.Sub(embeddedMigrations, path.Join(root, string(dialect)))
	if err != nil {
		return nil, fmt.Errorf("load %s migrations: %w", dialect, err)
	}
	providerOptions := append([]goose.ProviderOption{
		goose.WithDisableGlobalRegistry(true),
		// The master and v0.4 lines number their migrations apart (master
		// 00021 activity_log, v0.4 00019-00020 Live TV). A database that
		// applied a master migration first then gets the lower v0.4 versions.
		goose.WithAllowOutofOrder(true),
		goose.WithGoMigrations(foundationMigration()),
	}, extra...)
	provider, err := goose.NewProvider(gooseDialect, db, migrations, providerOptions...)
	if err != nil {
		return nil, fmt.Errorf("create %s migration provider: %w", dialect, err)
	}
	return provider, nil
}

func validateTwinsFS(fsys fs.FS) (string, error) {
	if fsys == nil {
		return "", errors.New("migration filesystem is required")
	}
	root, err := migrationRoot(fsys)
	if err != nil {
		return "", err
	}
	sqliteFiles, err := migrationFiles(fsys, path.Join(root, string(SQLite)))
	if err != nil {
		return "", err
	}
	postgresFiles, err := migrationFiles(fsys, path.Join(root, string(Postgres)))
	if err != nil {
		return "", err
	}
	for name := range sqliteFiles {
		if _, ok := postgresFiles[name]; !ok {
			return "", fmt.Errorf("SQLite migration %q has no PostgreSQL twin", name)
		}
	}
	for name := range postgresFiles {
		if _, ok := sqliteFiles[name]; !ok {
			return "", fmt.Errorf("PostgreSQL migration %q has no SQLite twin", name)
		}
	}
	return root, nil
}

func migrationRoot(fsys fs.FS) (string, error) {
	for _, root := range []string{"migrations", "."} {
		if isDirectory(fsys, path.Join(root, string(SQLite))) && isDirectory(fsys, path.Join(root, string(Postgres))) {
			return root, nil
		}
	}
	return "", errors.New("migration filesystem must contain sqlite and postgres directories")
}

func isDirectory(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.IsDir()
}

func migrationFiles(fsys fs.FS, directory string) (map[string]struct{}, error) {
	entries, err := fs.ReadDir(fsys, directory)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", directory, err)
	}
	files := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files[entry.Name()] = struct{}{}
	}
	return files, nil
}

func foundationMigration() *goose.Migration {
	return goose.NewGoMigration(2,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO store_metadata (key, value) VALUES ('migration', 'go-func')`)
			return err
		}},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM store_metadata WHERE key = 'migration'`)
			return err
		}},
	)
}
