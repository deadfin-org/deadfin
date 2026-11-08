// SPDX-License-Identifier: GPL-2.0-only

package postgres

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dialect implements the PostgreSQL-specific portions of store.Dialect.
type Dialect struct{}

func (Dialect) Placeholder(index int) string { return "$" + strconv.Itoa(index) }

func (Dialect) Boolean(value bool) string {
	if value {
		return "TRUE"
	}
	return "FALSE"
}

func (Dialect) TimeValue(value time.Time) any { return value.UTC() }

func (Dialect) ScanTime(value any) (time.Time, error) {
	switch value := value.(type) {
	case time.Time:
		return value.UTC(), nil
	case *time.Time:
		if value == nil {
			return time.Time{}, fmt.Errorf("PostgreSQL datetime scan value is nil")
		}
		return value.UTC(), nil
	case string:
		return parseTime(value)
	case []byte:
		return parseTime(string(value))
	default:
		return time.Time{}, fmt.Errorf("PostgreSQL datetime scan value has type %T", value)
	}
}

func (Dialect) Random() string { return "RANDOM()" }

func (Dialect) NullsLast() string { return "NULLS LAST" }

func (Dialect) TextCollation(expression string) string {
	return expression + ` COLLATE "C"`
}

func (Dialect) EscapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(value)
}

func (Dialect) SearchClause(column, placeholder string) string {
	return column + " ILIKE " + placeholder + ` ESCAPE '\'`
}

func (Dialect) ContainsClause(column, placeholder string) string {
	return "strpos(" + column + ", " + placeholder + ") > 0"
}

func parseTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999 -07:00",
	} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse PostgreSQL datetime %q", value)
}
