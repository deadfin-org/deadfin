// SPDX-License-Identifier: GPL-2.0-only

// Package activitylog records server activity, persists it, and publishes
// newly committed entries to WebSocket feed subscribers. It ports
// Jellyfin.Server.Implementations/Activity/ActivityManager.cs.
package activitylog

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/events"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Entry is the Jellyfin ActivityLogEntry wire shape.
type Entry struct {
	ID                  int64    `json:"Id"`
	Name                string   `json:"Name"`
	Overview            *string  `json:"Overview,omitzero"`
	ShortOverview       *string  `json:"ShortOverview,omitzero"`
	Type                string   `json:"Type"`
	ItemID              *string  `json:"ItemId,omitzero"`
	Date                dto.Time `json:"Date"`
	UserID              dto.UUID `json:"UserId"`
	UserPrimaryImageTag *string  `json:"UserPrimaryImageTag,omitzero"`
	Severity            string   `json:"Severity"`
}

// Query selects entries. Entries come newest first. A nil Limit returns every
// matching entry from StartIndex.
type Query struct {
	MinDate    *time.Time
	MaxDate    *time.Time
	HasUserID  *bool
	ItemID     *string
	Severity   *string
	StartIndex int
	Limit      *int
}

// Page is one page of entries and the count of every matching entry.
type Page struct {
	Entries          []Entry
	TotalRecordCount int
}

// Store persists entries. *store.ActivityLogStore satisfies it.
type Store interface {
	Insert(context.Context, store.ActivityLogEntry) (store.ActivityLogEntry, error)
	Query(context.Context, store.ActivityLogQuery) (store.ActivityLogPage, error)
	DeleteThrough(context.Context, time.Time) (int, error)
}

// recordQueue bounds the entries that Record holds for the writer worker.
const recordQueue = 256

// Log owns the activity store seam, the writer worker for queued entries, and
// the non-blocking event bus.
type Log struct {
	clock  clock.Clock
	store  Store
	logger *slog.Logger
	bus    *events.Bus[Entry]

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan Entry
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
}

// New constructs a process-local activity log on the injected clock. Tests
// and tools that have no database use it.
func New(c clock.Clock) *Log {
	return NewWithStore(c, &memoryStore{}, nil)
}

// NewWithStore constructs an activity log that persists through s. The log
// starts one writer worker for Record; Close stops it.
func NewWithStore(c clock.Clock, s Store, logger *slog.Logger) *Log {
	if c == nil {
		panic("activitylog: clock is required")
	}
	if s == nil {
		panic("activitylog: store is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := &Log{
		clock:  c,
		store:  s,
		logger: logger,
		bus:    events.NewBus[Entry](c),
		ctx:    ctx,
		cancel: cancel,
		queue:  make(chan Entry, recordQueue),
		done:   make(chan struct{}),
	}
	go log.run()
	return log
}

// Write commits an entry and then publishes it to current subscribers
// (ActivityManager.CreateAsync).
func (log *Log) Write(ctx context.Context, entry Entry) (Entry, error) {
	if entry.Name == "" {
		return Entry{}, errors.New("activitylog: name is required")
	}
	if entry.Type == "" {
		return Entry{}, errors.New("activitylog: type is required")
	}
	log.mu.RLock()
	defer log.mu.RUnlock()
	if log.closed {
		return Entry{}, errors.New("activitylog: log is closed")
	}
	if entry.Date.Time.IsZero() {
		entry.Date = dto.NewTime(log.clock.Now())
	}
	if entry.Severity == "" {
		entry.Severity = SeverityInformation
	}
	stored, err := log.store.Insert(ctx, toStore(entry))
	if err != nil {
		return Entry{}, err
	}
	entry = fromStore(stored)
	log.bus.Publish(cloneEntry(entry))
	return entry, nil
}

// Record stamps the entry with the current time and queues it for the writer
// worker. It does not wait for the database, so a request path never waits on
// the single SQLite writer. A full queue or a closed log drops the entry and
// logs a warning.
func (log *Log) Record(entry Entry) {
	if entry.Date.Time.IsZero() {
		entry.Date = dto.NewTime(log.clock.Now())
	}
	log.mu.RLock()
	defer log.mu.RUnlock()
	if log.closed {
		log.logger.Warn("activity log closed; entry dropped", "type", entry.Type, "name", entry.Name)
		return
	}
	select {
	case log.queue <- entry:
	default:
		log.logger.Warn("activity log queue full; entry dropped", "type", entry.Type, "name", entry.Name)
	}
}

func (log *Log) run() {
	defer close(log.done)
	for entry := range log.queue {
		if _, err := log.write(entry); err != nil {
			log.logger.Error("write activity log entry", "type", entry.Type, "name", entry.Name, "error", err)
		}
	}
}

// write is Write for the worker: Close holds the lock while it drains the
// queue, so the worker must not take it.
func (log *Log) write(entry Entry) (Entry, error) {
	if entry.Name == "" || entry.Type == "" {
		return Entry{}, errors.New("activitylog: name and type are required")
	}
	if entry.Severity == "" {
		entry.Severity = SeverityInformation
	}
	stored, err := log.store.Insert(log.ctx, toStore(entry))
	if err != nil {
		return Entry{}, err
	}
	entry = fromStore(stored)
	log.bus.Publish(cloneEntry(entry))
	return entry, nil
}

// Query returns the entries that match query, newest first.
func (log *Log) Query(ctx context.Context, query Query) (Page, error) {
	page, err := log.store.Query(ctx, store.ActivityLogQuery{
		MinDate:    query.MinDate,
		MaxDate:    query.MaxDate,
		HasUserID:  query.HasUserID,
		ItemID:     query.ItemID,
		Severity:   query.Severity,
		StartIndex: query.StartIndex,
		Limit:      query.Limit,
	})
	if err != nil {
		return Page{}, err
	}
	result := Page{Entries: make([]Entry, 0, len(page.Entries)), TotalRecordCount: page.TotalRecordCount}
	for _, entry := range page.Entries {
		result.Entries = append(result.Entries, fromStore(entry))
	}
	return result, nil
}

// Prune deletes the entries created at or before cut and returns the count
// deleted (ActivityManager.CleanAsync: DateCreated <= startDate).
func (log *Log) Prune(ctx context.Context, cut time.Time) (int, error) {
	log.mu.RLock()
	defer log.mu.RUnlock()
	if log.closed {
		return 0, errors.New("activitylog: log is closed")
	}
	return log.store.DeleteThrough(ctx, cut)
}

// Subscribe registers for newly committed entries.
func (log *Log) Subscribe(options events.SubscriptionOptions) *events.Subscription[Entry] {
	return log.bus.Subscribe(options)
}

// Close rejects future writes, writes the queued entries, stops the worker,
// and stops every subscription. When ctx ends first, Close cancels the
// pending writes and waits for the worker to stop.
func (log *Log) Close(ctx context.Context) error {
	log.mu.Lock()
	if !log.closed {
		log.closed = true
		close(log.queue)
	}
	log.mu.Unlock()
	var err error
	select {
	case <-log.done:
	case <-ctx.Done():
		err = ctx.Err()
		log.cancel()
		<-log.done
	}
	log.cancel()
	return errors.Join(err, log.bus.Close(ctx))
}

func toStore(entry Entry) store.ActivityLogEntry {
	return store.ActivityLogEntry{
		ID:            entry.ID,
		Name:          entry.Name,
		Overview:      cloneString(entry.Overview),
		ShortOverview: cloneString(entry.ShortOverview),
		Type:          entry.Type,
		UserID:        entry.UserID,
		ItemID:        cloneString(entry.ItemID),
		DateCreated:   entry.Date.Time,
		Severity:      entry.Severity,
	}
}

func fromStore(entry store.ActivityLogEntry) Entry {
	return Entry{
		ID:            entry.ID,
		Name:          entry.Name,
		Overview:      cloneString(entry.Overview),
		ShortOverview: cloneString(entry.ShortOverview),
		Type:          entry.Type,
		ItemID:        cloneString(entry.ItemID),
		Date:          dto.NewTime(entry.DateCreated),
		UserID:        entry.UserID,
		Severity:      entry.Severity,
	}
}

func cloneEntry(entry Entry) Entry {
	entry.Overview = cloneString(entry.Overview)
	entry.ShortOverview = cloneString(entry.ShortOverview)
	entry.ItemID = cloneString(entry.ItemID)
	entry.UserPrimaryImageTag = cloneString(entry.UserPrimaryImageTag)
	return entry
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// memoryStore is the process-local Store behind New.
type memoryStore struct {
	mu      sync.Mutex
	nextID  int64
	entries []store.ActivityLogEntry
}

func (m *memoryStore) Insert(_ context.Context, entry store.ActivityLogEntry) (store.ActivityLogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	entry.ID = m.nextID
	entry.DateCreated = entry.DateCreated.UTC()
	m.entries = append(m.entries, entry)
	return entry, nil
}

func (m *memoryStore) Query(_ context.Context, query store.ActivityLogQuery) (store.ActivityLogPage, error) {
	if query.StartIndex < 0 || (query.Limit != nil && *query.Limit < 0) {
		return store.ActivityLogPage{}, errors.New("activitylog: negative paging")
	}
	m.mu.Lock()
	matched := make([]store.ActivityLogEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		if matchesStoreQuery(entry, query) {
			matched = append(matched, entry)
		}
	}
	m.mu.Unlock()
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].DateCreated.Equal(matched[j].DateCreated) {
			return matched[i].DateCreated.After(matched[j].DateCreated)
		}
		return matched[i].ID > matched[j].ID
	})
	page := store.ActivityLogPage{TotalRecordCount: len(matched), Entries: []store.ActivityLogEntry{}}
	for index := query.StartIndex; index < len(matched); index++ {
		if query.Limit != nil && len(page.Entries) >= *query.Limit {
			break
		}
		page.Entries = append(page.Entries, matched[index])
	}
	return page, nil
}

func (m *memoryStore) DeleteThrough(_ context.Context, cut time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.entries[:0]
	removed := 0
	for _, entry := range m.entries {
		if !entry.DateCreated.After(cut) {
			removed++
			continue
		}
		kept = append(kept, entry)
	}
	m.entries = kept
	return removed, nil
}

func matchesStoreQuery(entry store.ActivityLogEntry, query store.ActivityLogQuery) bool {
	if query.HasUserID != nil && *query.HasUserID == entry.UserID.IsNil() {
		return false
	}
	if query.MinDate != nil && entry.DateCreated.Before(*query.MinDate) {
		return false
	}
	if query.MaxDate != nil && entry.DateCreated.After(*query.MaxDate) {
		return false
	}
	if query.ItemID != nil && (entry.ItemID == nil || *entry.ItemID != *query.ItemID) {
		return false
	}
	if query.Severity != nil && entry.Severity != *query.Severity {
		return false
	}
	return true
}

// Recorder queues entries for the activity log. *Log satisfies it.
type Recorder interface {
	Record(Entry)
}
