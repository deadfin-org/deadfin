// SPDX-License-Identifier: GPL-2.0-only

package sqlite

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect implements the SQLite-specific portions of store.Dialect.
type Dialect struct{}

func (Dialect) Placeholder(_ int) string { return "?" }

func (Dialect) Boolean(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (Dialect) TimeValue(value time.Time) any {
	return value.UTC().UnixMicro()
}

func (Dialect) ScanTime(value any) (time.Time, error) {
	micros, err := sqliteMicros(value)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMicro(micros).UTC(), nil
}

func (Dialect) Random() string { return "RANDOM()" }

func (Dialect) NullsLast() string { return "NULLS LAST" }

func (Dialect) TextCollation(expression string) string {
	return expression + " COLLATE BINARY"
}

func (Dialect) EscapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(value)
}

func (Dialect) SearchClause(column, placeholder string) string {
	return column + " LIKE " + placeholder + ` ESCAPE '\'`
}

func (Dialect) ContainsClause(column, placeholder string) string {
	return "instr(" + column + ", " + placeholder + ") > 0"
}

func sqliteMicros(value any) (int64, error) {
	switch value := value.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	case string:
		return strconv.ParseInt(value, 10, 64)
	default:
		return 0, fmt.Errorf("SQLite datetime scan value has type %T", value)
	}
}
