// SPDX-License-Identifier: GPL-2.0-only

// Package postgres provides the PostgreSQL persistence backend.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	writerMaxOpenConnections  = 1
	readerMaxOpenConnections  = 16
	migrateMaxOpenConnections = 2
	maxConnectionLifetime     = time.Hour
	maxConnectionIdleTime     = 15 * time.Minute
)

// Config configures a PostgreSQL store. DSN uses pgx connection-string syntax.
//
// Schema is an optional PostgreSQL schema name. When supplied, Open creates it
// if needed, confines migrations and queries to it through search_path, and
// leaves its ownership and cleanup to the caller. A unique schema per live test
// gives storetest isolation without process-global configuration.
type Config struct {
	DSN    string
	Schema string
}

// Open connects to PostgreSQL, configures distinct writer and reader pools, and
// applies the embedded PostgreSQL migrations before returning the store.
func Open(ctx context.Context, config Config) (store.Stores, error) {
	if strings.TrimSpace(config.DSN) == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	if strings.ContainsRune(config.Schema, '\x00') {
		return nil, errors.New("PostgreSQL schema must not contain NUL")
	}

	if config.Schema != "" {
		if err := ensureSchema(ctx, config.DSN, config.Schema); err != nil {
			return nil, err
		}
	}

	writer, err := openDatabase(config.DSN, config.Schema, writerMaxOpenConnections)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL writer: %w", err)
	}
	reader, err := openDatabase(config.DSN, config.Schema, readerMaxOpenConnections)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open PostgreSQL reader: %w", err)
	}
	closePools := func() {
		_ = reader.Close()
		_ = writer.Close()
	}

	if err := writer.PingContext(ctx); err != nil {
		closePools()
		return nil, fmt.Errorf("connect PostgreSQL writer: %w", err)
	}
	if err := reader.PingContext(ctx); err != nil {
		closePools()
		return nil, fmt.Errorf("connect PostgreSQL reader: %w", err)
	}
	if err := migrateLocked(ctx, config.DSN, config.Schema); err != nil {
		closePools()
		return nil, err
	}
	opened := &stores{writer: writer, reader: reader, dialect: Dialect{}}
	if err := store.RepairOutdatedDerivedState(ctx, opened); err != nil {
		closePools()
		return nil, fmt.Errorf("repair PostgreSQL outdated derived state: %w", err)
	}
	if err := store.BackfillMissingItemUserDataKeys(ctx, opened); err != nil {
		closePools()
		return nil, fmt.Errorf("backfill PostgreSQL user data keys: %w", err)
	}
	return opened, nil
}

// migrateLocked applies the migrations under a session advisory lock, so
// servers that start at the same time against one database do not race. The
// lock ID derives from the effective search_path, so stores in separate
// schemas do not wait on each other. The run uses its own small pool: the
// lock holds one connection and the migrations use another.
func migrateLocked(ctx context.Context, dsn, schema string) (err error) {
	config, err := connectionConfig(dsn, schema)
	if err != nil {
		return err
	}
	db := openPool(config, migrateMaxOpenConnections)
	defer func() { err = errors.Join(err, db.Close()) }()
	lockID := migrationLockID(config.RuntimeParams["search_path"])
	if err := migrate.Up(ctx, db, migrate.Postgres, migrate.WithAdvisoryLock(lockID)); err != nil {
		return fmt.Errorf("migrate PostgreSQL store: %w", err)
	}
	return nil
}

func migrationLockID(searchPath string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("deadfin-migrate\x00" + searchPath))
	return int64(hash.Sum64())
}

func ensureSchema(ctx context.Context, dsn, schema string) (err error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	admin := stdlib.OpenDB(*config)
	defer func() { err = errors.Join(err, admin.Close()) }()
	if err := admin.PingContext(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL schema administrator: %w", err)
	}
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+identifier); err != nil {
		return fmt.Errorf("create PostgreSQL schema %q: %w", schema, err)
	}
	return nil
}

func openDatabase(dsn, schema string, maxOpenConnections int) (*sql.DB, error) {
	config, err := connectionConfig(dsn, schema)
	if err != nil {
		return nil, err
	}
	return openPool(config, maxOpenConnections), nil
}

func connectionConfig(dsn, schema string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	if schema != "" {
		config.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	}
	// Deadfin's queries are short and run often. JIT compilation adds about
	// 0.8 s to a large item query and never pays back, so sessions turn it
	// off unless the DSN sets jit.
	if _, ok := config.RuntimeParams["jit"]; !ok {
		config.RuntimeParams["jit"] = "off"
	}
	return config, nil
}

func openPool(config *pgx.ConnConfig, maxOpenConnections int) *sql.DB {
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	db.SetConnMaxLifetime(maxConnectionLifetime)
	db.SetConnMaxIdleTime(maxConnectionIdleTime)
	return db
}

type stores struct {
	writer  *sql.DB
	reader  *sql.DB
	dialect store.Dialect
}

func (s *stores) Writer() *sql.DB { return s.writer }

func (s *stores) Reader() *sql.DB { return s.reader }

func (s *stores) Dialect() store.Dialect { return s.dialect }

func (s *stores) Close() error {
	return errors.Join(s.reader.Close(), s.writer.Close())
}
