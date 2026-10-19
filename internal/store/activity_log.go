// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// ActivityLogEntry is one persisted activity log row (upstream ActivityLog,
// Jellyfin.Database.Implementations/Entities/ActivityLog.cs). A nil UserID
// means the entry has no user.
type ActivityLogEntry struct {
	ID            int64
	Name          string
	Overview      *string
	ShortOverview *string
	Type          string
	UserID        dto.UUID
	ItemID        *string
	DateCreated   time.Time
	Severity      string
}

// ActivityLogQuery selects activity log rows. Rows come newest first
// (date_created, then id, descending), the upstream default order
// (ActivityManager.cs ApplyOrdering). A nil Limit returns every matching row.
type ActivityLogQuery struct {
	MinDate    *time.Time
	MaxDate    *time.Time
	HasUserID  *bool
	ItemID     *string
	Severity   *string
	StartIndex int
	Limit      *int
}

// ActivityLogPage is one page of rows and the count of every matching row.
type ActivityLogPage struct {
	Entries          []ActivityLogEntry
	TotalRecordCount int
}

// ActivityLogStore persists the server activity log.
type ActivityLogStore struct{ stores Stores }

// NewActivityLogStore constructs the activity log adapter.
func NewActivityLogStore(stores Stores) *ActivityLogStore {
	return &ActivityLogStore{stores: stores}
}

func (s *ActivityLogStore) p(index int) string { return s.stores.Dialect().Placeholder(index) }

func (s *ActivityLogStore) userValue(id dto.UUID) any {
	if id.IsNil() {
		return nil
	}
	if s.p(1) == "$1" {
		return id.Dashed()
	}
	return id[:]
}

// Insert stores entry and returns it with its assigned ID.
func (s *ActivityLogStore) Insert(ctx context.Context, entry ActivityLogEntry) (ActivityLogEntry, error) {
	if entry.Name == "" || entry.Type == "" || entry.Severity == "" {
		return ActivityLogEntry{}, errors.New("insert activity log entry: name, type and severity are required")
	}
	entry.DateCreated = entry.DateCreated.UTC()
	err := s.stores.Writer().QueryRowContext(ctx,
		"INSERT INTO activity_log (name, overview, short_overview, type, user_id, item_id, date_created, severity) VALUES ("+
			s.p(1)+", "+s.p(2)+", "+s.p(3)+", "+s.p(4)+", "+s.p(5)+", "+s.p(6)+", "+s.p(7)+", "+s.p(8)+") RETURNING id",
		entry.Name, entry.Overview, entry.ShortOverview, entry.Type, s.userValue(entry.UserID), entry.ItemID,
		s.stores.Dialect().TimeValue(entry.DateCreated), entry.Severity,
	).Scan(&entry.ID)
	if err != nil {
		return ActivityLogEntry{}, fmt.Errorf("insert activity log entry: %w", err)
	}
	return entry, nil
}

// Query returns the rows that match query, newest first, and the total count.
func (s *ActivityLogStore) Query(ctx context.Context, query ActivityLogQuery) (ActivityLogPage, error) {
	if query.StartIndex < 0 || (query.Limit != nil && *query.Limit < 0) {
		return ActivityLogPage{}, errors.New("query activity log: negative paging")
	}
	dialect := s.stores.Dialect()
	var where []string
	var args []any
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, s.p(len(args))))
	}
	if query.HasUserID != nil {
		if *query.HasUserID {
			where = append(where, "user_id IS NOT NULL")
		} else {
			where = append(where, "user_id IS NULL")
		}
	}
	if query.MinDate != nil {
		add("date_created >= %s", dialect.TimeValue(query.MinDate.UTC()))
	}
	if query.MaxDate != nil {
		add("date_created <= %s", dialect.TimeValue(query.MaxDate.UTC()))
	}
	if query.ItemID != nil {
		add("item_id = %s", *query.ItemID)
	}
	if query.Severity != nil {
		add("severity = %s", *query.Severity)
	}
	filter := ""
	if len(where) > 0 {
		filter = " WHERE " + strings.Join(where, " AND ")
	}

	reader := s.stores.Reader()
	var page ActivityLogPage
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM activity_log"+filter, args...).Scan(&page.TotalRecordCount); err != nil {
		return ActivityLogPage{}, fmt.Errorf("count activity log entries: %w", err)
	}
	statement := "SELECT id, name, overview, short_overview, type, user_id, item_id, date_created, severity FROM activity_log" +
		filter + " ORDER BY date_created DESC, id DESC"
	skip := query.StartIndex
	if query.Limit != nil {
		args = append(args, *query.Limit, query.StartIndex)
		statement += " LIMIT " + s.p(len(args)-1) + " OFFSET " + s.p(len(args))
		skip = 0
	}
	rows, err := reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return ActivityLogPage{}, fmt.Errorf("query activity log entries: %w", err)
	}
	defer rows.Close()
	page.Entries = []ActivityLogEntry{}
	for rows.Next() {
		entry, err := s.scanEntry(rows)
		if err != nil {
			return ActivityLogPage{}, err
		}
		if skip > 0 {
			skip--
			continue
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return ActivityLogPage{}, fmt.Errorf("query activity log entries: %w", err)
	}
	return page, nil
}

func (s *ActivityLogStore) scanEntry(rows *sql.Rows) (ActivityLogEntry, error) {
	var (
		entry                   ActivityLogEntry
		overview, shortOverview sql.NullString
		itemID                  sql.NullString
		userID, dateCreated     any
	)
	if err := rows.Scan(&entry.ID, &entry.Name, &overview, &shortOverview, &entry.Type, &userID, &itemID, &dateCreated, &entry.Severity); err != nil {
		return ActivityLogEntry{}, fmt.Errorf("scan activity log entry: %w", err)
	}
	if userID != nil {
		if err := scanIdentityUUID(&entry.UserID, userID); err != nil {
			return ActivityLogEntry{}, fmt.Errorf("scan activity log user: %w", err)
		}
	}
	created, err := s.stores.Dialect().ScanTime(dateCreated)
	if err != nil {
		return ActivityLogEntry{}, fmt.Errorf("scan activity log date: %w", err)
	}
	entry.DateCreated = created
	entry.Overview = nullStringPointer(overview)
	entry.ShortOverview = nullStringPointer(shortOverview)
	entry.ItemID = nullStringPointer(itemID)
	return entry, nil
}

// DeleteThrough deletes the rows created at or before cut and returns the
// count deleted (ActivityManager.CleanAsync: DateCreated <= startDate).
func (s *ActivityLogStore) DeleteThrough(ctx context.Context, cut time.Time) (int, error) {
	result, err := s.stores.Writer().ExecContext(ctx,
		"DELETE FROM activity_log WHERE date_created <= "+s.p(1), s.stores.Dialect().TimeValue(cut.UTC()))
	if err != nil {
		return 0, fmt.Errorf("delete activity log entries: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete activity log entries: %w", err)
	}
	return int(count), nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}
