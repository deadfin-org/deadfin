// SPDX-License-Identifier: GPL-2.0-only

package storetest

import (
	"database/sql"
	"time"

	"github.com/deadfin-org/deadfin/internal/store"
)

// UnsupportedStores returns a minimal store.Stores that owns no database and
// does not implement store.Maintainer. Tests use this helper for a backend
// the maintainer adapter must log-skip. The helper keeps database/sql inside
// the store boundary.
//
// The returned stores return nil connections and a SQLite-shaped dialect.
// The stores do not run any SQL. Callers must not use the returned stores
// for read or write operations.
func UnsupportedStores() store.Stores {
	return unsupportedStores{}
}

type unsupportedStores struct{}

func (unsupportedStores) Writer() *sql.DB        { return nil }
func (unsupportedStores) Reader() *sql.DB        { return nil }
func (unsupportedStores) Dialect() store.Dialect { return unsupportedDialect{} }
func (unsupportedStores) Close() error           { return nil }

type unsupportedDialect struct{}

func (unsupportedDialect) Placeholder(int) string               { return "?" }
func (unsupportedDialect) Boolean(bool) string                  { return "0" }
func (unsupportedDialect) TimeValue(t time.Time) any            { return t }
func (unsupportedDialect) ScanTime(v any) (time.Time, error)    { return time.Time{}, nil }
func (unsupportedDialect) Random() string                       { return "RANDOM()" }
func (unsupportedDialect) NullsLast() string                    { return "" }
func (unsupportedDialect) TextCollation(string) string          { return "" }
func (unsupportedDialect) EscapeLike(string) string             { return "" }
func (unsupportedDialect) SearchClause(string, string) string   { return "" }
func (unsupportedDialect) ContainsClause(string, string) string { return "" }
func (unsupportedDialect) SpanAtMost(string, string, time.Duration) string {
	return ""
}
