// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"time"
)

// Stores owns the database handles used by typed persistence implementations.
// Mutations must use Writer; reads must use Reader.
type Stores interface {
	Writer() *sql.DB
	Reader() *sql.DB
	Dialect() Dialect
	Close() error
}

// Dialect contains the SQL fragments and values that differ between backends.
// Query builders use it so fixed-shape SQL remains portable from the first query.
type Dialect interface {
	Placeholder(index int) string
	Boolean(value bool) string
	TimeValue(value time.Time) any
	ScanTime(value any) (time.Time, error)
	Random() string
	NullsLast() string
	// TextCollation pins SQLite BINARY byte ordering on every backend.
	TextCollation(expression string) string
	EscapeLike(value string) string
	SearchClause(column, placeholder string) string
	// ContainsClause tests that column holds the placeholder text as a
	// literal, case-sensitive substring. It ports the EF Core translation of
	// string.Contains.
	ContainsClause(column, placeholder string) string
}

// Maintainer runs backend-specific database maintenance (SQLite: PRAGMA
// wal_checkpoint(TRUNCATE) → PRAGMA optimize → VACUUM → PRAGMA
// wal_checkpoint(TRUNCATE), in upstream order — SqliteDatabaseProvider.cs:107-110).
// Writer MaxOpenConns=1 serializes against all other writes — no lock
// coordination needed.
type Maintainer interface {
	Maintain(ctx context.Context) error
}

// StatisticsRefresher keeps query planner statistics current. The SQLite
// backend implements it; PostgreSQL autovacuum analyzes changed tables by
// itself and needs no request.
type StatisticsRefresher interface {
	// RequestStatisticsRefresh asks for a refresh soon after a write that
	// changed many rows. It returns at once.
	RequestStatisticsRefresh()
}
