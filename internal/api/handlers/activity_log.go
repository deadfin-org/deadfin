// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/deadfin-org/deadfin/internal/activitylog"
	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// ActivityLogReader supplies the persisted activity log for the REST log
// endpoint. *activitylog.Log satisfies this interface.
type ActivityLogReader interface {
	Query(context.Context, activitylog.Query) (activitylog.Page, error)
}

// RegisterActivityLog registers the GET /System/ActivityLog/Entries route
// (GetLogEntries) with the RequiresElevation policy. Passing a nil log
// registers the route with a 503 handler, which keeps the pure route ledger
// path valid.
func RegisterActivityLog(rt *router.Router, log ActivityLogReader, users ItemUserResolver) {
	policy := string(auth.PolicyRequiresElevation)
	h := activityLogHandler{log: log, users: users}
	rt.Handle(http.MethodGet, "/System/ActivityLog/Entries", router.Route{
		OperationID: "GetLogEntries",
		Policy:      policy,
	}, h.entries)
}

type activityLogHandler struct {
	log   ActivityLogReader
	users ItemUserResolver
}

// activityLogQuery binds the query parameters for GetLogEntries through the
// model-binding convention. Pointer fields bind as optional.
type activityLogQuery struct {
	StartIndex    *int              `query:"startIndex"`
	Limit         *int              `query:"limit"`
	MinDate       *dto.Time         `query:"minDate"`
	MaxDate       *dto.Time         `query:"maxDate"`
	HasUserID     *bool             `query:"hasUserId"`
	Name          *string           `query:"name"`
	Overview      *string           `query:"overview"`
	ShortOverview *string           `query:"shortOverview"`
	Type          *string           `query:"type"`
	Username      *string           `query:"username"`
	ItemID        *dto.UUID         `query:"itemId"`
	Severity      *activitySeverity `query:"severity"`
	SortBy        []activitySortKey `query:"sortBy"`
	SortOrder     []activityOrder   `query:"sortOrder"`
}

// activitySeverity validates the seven pinned LogLevel values case-insensitively.
type activitySeverity string

var activitySeverityValues = map[string]activitySeverity{
	"trace":       "Trace",
	"debug":       "Debug",
	"information": "Information",
	"warning":     "Warning",
	"error":       "Error",
	"critical":    "Critical",
	"none":        "None",
}

func (s *activitySeverity) UnmarshalText(text []byte) error {
	v, ok := activitySeverityValues[strings.ToLower(string(text))]
	if !ok {
		return errors.New("invalid severity value")
	}
	*s = v
	return nil
}

// activitySortKey validates the pinned ActivityLogSortBy values, including the
// upstream misspelling "Overiew".
type activitySortKey string

var activitySortKeyValues = map[string]activitySortKey{
	"name":          "Name",
	"overiew":       "Overiew",
	"shortoverview": "ShortOverview",
	"type":          "Type",
	"datecreated":   "DateCreated",
	"username":      "Username",
	"logseverity":   "LogSeverity",
}

func (s *activitySortKey) UnmarshalText(text []byte) error {
	v, ok := activitySortKeyValues[strings.ToLower(string(text))]
	if !ok {
		return errors.New("invalid sortBy value")
	}
	*s = v
	return nil
}

// activityOrder validates ascending or descending sort directions.
type activityOrder string

var activityOrderValues = map[string]activityOrder{
	"ascending":  "Ascending",
	"descending": "Descending",
}

func (o *activityOrder) UnmarshalText(text []byte) error {
	v, ok := activityOrderValues[strings.ToLower(string(text))]
	if !ok {
		return errors.New("invalid sortOrder value")
	}
	*o = v
	return nil
}

const (
	activityOrderAscending  activityOrder = "Ascending"
	activityOrderDescending activityOrder = "Descending"
)

// logSeverityOrder maps the LogLevel ordinal used for LogSeverity sorting
// (Trace=0 through None=6).
var logSeverityOrder = map[string]int{
	"Trace":       0,
	"Debug":       1,
	"Information": 2,
	"Warning":     3,
	"Error":       4,
	"Critical":    5,
	"None":        6,
}

func (h activityLogHandler) entries(w http.ResponseWriter, request *http.Request) {
	if h.log == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	var q activityLogQuery
	if err := bind.QueryRequest(request, &q); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	// Divergence: upstream EF throws 500 on negative Skip/Take. Deadfin returns
	// 400 because negative paging is invalid client input that no pinned client
	// sends; a 400 diagnosis is clearer than a 500.
	if (q.StartIndex != nil && *q.StartIndex < 0) || (q.Limit != nil && *q.Limit < 0) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	startIndex := 0
	if q.StartIndex != nil {
		startIndex = *q.StartIndex
	}
	limit := 100
	if q.Limit != nil {
		limit = *q.Limit
	}

	// The store applies the exact predicates. When the request has no text
	// filter and no explicit sort, the store also pages in the default
	// newest-first order (ActivityManager.cs GetPagedResultAsync).
	stored := activitylog.Query{HasUserID: q.HasUserID, Severity: (*string)(q.Severity)}
	if q.MinDate != nil {
		stored.MinDate = &q.MinDate.Time
	}
	if q.MaxDate != nil {
		stored.MaxDate = &q.MaxDate.Time
	}
	if q.ItemID != nil {
		itemID := q.ItemID.String()
		stored.ItemID = &itemID
	}
	if q.Name == nil && q.Overview == nil && q.ShortOverview == nil && q.Type == nil && q.Username == nil && len(q.SortBy) == 0 {
		stored.StartIndex = startIndex
		stored.Limit = &limit
		result, err := h.log.Query(request.Context(), stored)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		page := dto.NewQueryResult[activitylog.Entry]()
		page.Items = append(page.Items, result.Entries...)
		page.TotalRecordCount = result.TotalRecordCount
		page.StartIndex = startIndex
		_ = render.JSON(w, request, http.StatusOK, page)
		return
	}

	result, err := h.log.Query(request.Context(), stored)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	filtered := filterActivityEntries(request.Context(), result.Entries, &q, h.users, w)
	if filtered == nil {
		return // error already written
	}

	totalRecordCount := len(filtered)
	// Username sorts by the resolved user name (ActivityManager.cs:201). Resolve
	// every distinct non-zero UserID in the filtered set once, before the
	// stable sort, so the comparator does not repeat resolver calls. Unknown
	// users resolve to an empty name, which sorts first ascending (SQL
	// NULL-first). A resolver error other than not-found answers 500.
	var nameCache map[dto.UUID]string
	for _, k := range q.SortBy {
		if k == "Username" {
			nameCache = resolveActivityUsernames(request.Context(), filtered, h.users, w)
			if nameCache == nil {
				return // error already written
			}
			break
		}
	}
	sortActivityEntries(filtered, q.SortBy, q.SortOrder, nameCache)

	page := dto.NewQueryResult[activitylog.Entry]()
	for i := startIndex; i < len(filtered) && len(page.Items) < limit; i++ {
		page.Items = append(page.Items, filtered[i])
	}
	page.TotalRecordCount = totalRecordCount
	page.StartIndex = startIndex
	_ = render.JSON(w, request, http.StatusOK, page)
}

func filterActivityEntries(ctx context.Context, entries []activitylog.Entry, q *activityLogQuery, users ItemUserResolver, w http.ResponseWriter) []activitylog.Entry {
	var nameCache map[dto.UUID]string
	if q.Username != nil && users != nil {
		nameCache = make(map[dto.UUID]string)
	}

	result := make([]activitylog.Entry, 0, len(entries))
	for _, entry := range entries {
		if q.HasUserID != nil {
			hasUser := !entry.UserID.IsNil()
			if *q.HasUserID != hasUser {
				continue
			}
		}
		if q.MinDate != nil && entry.Date.Time.Before(q.MinDate.Time) {
			continue
		}
		if q.MaxDate != nil && entry.Date.Time.After(q.MaxDate.Time) {
			continue
		}
		if q.Name != nil && !strings.Contains(strings.ToLower(entry.Name), strings.ToLower(*q.Name)) {
			continue
		}
		if q.Overview != nil {
			if entry.Overview == nil || !strings.Contains(strings.ToLower(*entry.Overview), strings.ToLower(*q.Overview)) {
				continue
			}
		}
		if q.ShortOverview != nil {
			if entry.ShortOverview == nil || !strings.Contains(strings.ToLower(*entry.ShortOverview), strings.ToLower(*q.ShortOverview)) {
				continue
			}
		}
		if q.Type != nil && !strings.Contains(strings.ToLower(entry.Type), strings.ToLower(*q.Type)) {
			continue
		}
		if q.Severity != nil && !strings.EqualFold(entry.Severity, string(*q.Severity)) {
			continue
		}
		if q.ItemID != nil {
			if entry.ItemID == nil || *entry.ItemID != q.ItemID.String() {
				continue
			}
		}
		if q.Username != nil {
			if !matchActivityUsername(ctx, entry, q.Username, users, nameCache, w) {
				if w.Header().Get("Content-Type") != "" {
					return nil // error already written
				}
				continue
			}
		}
		result = append(result, entry)
	}
	return result
}

// matchActivityUsername resolves the entry's UserID through the resolver and
// applies a case-insensitive substring match on the user name. Returns false
// when the entry does not match. When a resolver error other than not-found
// occurs, writes a 500 response and returns false with the Content-Type set
// so the caller can detect the error.
func matchActivityUsername(ctx context.Context, entry activitylog.Entry, pattern *string, users ItemUserResolver, nameCache map[dto.UUID]string, w http.ResponseWriter) bool {
	if entry.UserID.IsNil() || users == nil {
		return false
	}
	if name, ok := nameCache[entry.UserID]; ok {
		return strings.Contains(strings.ToLower(name), strings.ToLower(*pattern))
	}
	user, err := users.Get(ctx, entry.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false
	case err != nil:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return false
	default:
		nameCache[entry.UserID] = user.Name
		return strings.Contains(strings.ToLower(user.Name), strings.ToLower(*pattern))
	}
}

// resolveActivityUsernames resolves the name of every distinct non-zero UserID
// in entries through the resolver, for Username sorting. Unknown users
// resolve to an empty name (SQL NULL-first). When users is nil, the returned
// cache is empty so every comparison treats the name as NULL. A resolver error
// other than not-found writes a 500 response and returns nil.
func resolveActivityUsernames(ctx context.Context, entries []activitylog.Entry, users ItemUserResolver, w http.ResponseWriter) map[dto.UUID]string {
	cache := make(map[dto.UUID]string)
	if users == nil {
		return cache
	}
	for _, entry := range entries {
		if entry.UserID.IsNil() {
			continue
		}
		if _, ok := cache[entry.UserID]; ok {
			continue
		}
		user, err := users.Get(ctx, entry.UserID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			cache[entry.UserID] = ""
		case err != nil:
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return nil
		default:
			cache[entry.UserID] = user.Name
		}
	}
	return cache
}

func sortActivityEntries(entries []activitylog.Entry, sortBy []activitySortKey, sortOrder []activityOrder, nameCache map[dto.UUID]string) {
	if len(sortBy) == 0 {
		// Default: Date descending. The snapshot is already newest-first; a
		// stable sort on the reversed comparator keeps that order.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Date.Time.After(entries[j].Date.Time)
		})
		return
	}
	firstOrder := activityOrderAscending
	if len(sortOrder) > 0 {
		firstOrder = sortOrder[0]
	}
	// Multi-key stable sort: apply the least significant key first.
	for idx := len(sortBy) - 1; idx >= 0; idx-- {
		key := sortBy[idx]
		order := firstOrder
		if idx < len(sortOrder) {
			order = sortOrder[idx]
		}
		descending := order == activityOrderDescending
		sort.SliceStable(entries, func(i, j int) bool {
			cmp := activityCompare(entries[i], entries[j], key, nameCache)
			if descending {
				return cmp > 0
			}
			return cmp < 0
		})
	}
}

func activityCompare(a, b activitylog.Entry, key activitySortKey, nameCache map[dto.UUID]string) int {
	switch key {
	case "Name":
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	case "Overiew":
		return strings.Compare(ptrToLower(a.Overview), ptrToLower(b.Overview))
	case "ShortOverview":
		return strings.Compare(ptrToLower(a.ShortOverview), ptrToLower(b.ShortOverview))
	case "Type":
		return strings.Compare(strings.ToLower(a.Type), strings.ToLower(b.Type))
	case "DateCreated":
		return a.Date.Time.Compare(b.Date.Time)
	case "LogSeverity":
		return logSeverityOrder[a.Severity] - logSeverityOrder[b.Severity]
	case "Username":
		// Sort by the resolved user name (ActivityManager.cs:201). A nil or
		// unknown UserID resolves to an empty name, which sorts first ascending
		// and last descending (SQL NULL-first). The cache is built once by
		// resolveActivityUsernames; a missing entry is an empty name.
		return strings.Compare(strings.ToLower(nameCache[a.UserID]), strings.ToLower(nameCache[b.UserID]))
	}
	return 0
}

func ptrToLower(s *string) string {
	if s == nil {
		return ""
	}
	return strings.ToLower(*s)
}
