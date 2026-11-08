// SPDX-License-Identifier: GPL-2.0-only

package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/migrate"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

func TestOpenMigratesUpAndDownForMemoryAndFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		path     string
		isMemory bool
	}{
		{name: "memory", path: ":memory:", isMemory: true},
		{name: "file", path: filepath.Join(t.TempDir(), "deadfin.db")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stores := openStores(t, tc.path)
			assertGoMigrationMarker(t, stores.Writer(), true)
			if tc.isMemory {
				assertGoMigrationMarker(t, stores.Reader(), true)

				const key, want = "memory-reader-visibility", "visible-through-reader"
				if _, err := stores.Writer().ExecContext(t.Context(), `INSERT INTO store_metadata (key, value) VALUES (?, ?)`, key, want); err != nil {
					t.Fatalf("creating memory writer row: %v", err)
				}
				var got string
				if err := stores.Reader().QueryRowContext(t.Context(), `SELECT value FROM store_metadata WHERE key = ?`, key).Scan(&got); err != nil {
					t.Fatalf("reading memory writer row through reader: %v", err)
				}
				if got != want {
					t.Fatalf("memory writer row through reader = %q, want %q", got, want)
				}
			}
			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 6); err != nil {
				t.Fatalf("rolling back 00007 item additional-parts migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "item_additional_parts", false)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_additional_parts_path", false)
			assertSQLiteTable(t, stores.Writer(), "item_user_data_keys", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_lookup", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_key", true)

			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 5); err != nil {
				t.Fatalf("rolling back 00006 item query-plan indexes migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "item_user_data_keys", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_lookup", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_key", true)

			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 4); err != nil {
				t.Fatalf("rolling back 00005 item user-data keys migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "item_user_data_keys", false)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_lookup", false)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_key", false)
			assertSQLiteTable(t, stores.Writer(), "items", true)

			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 3); err != nil {
				t.Fatalf("rolling back 00004 items migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "items", false)
			assertSQLiteTable(t, stores.Writer(), "users", true)

			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 2); err != nil {
				t.Fatalf("rolling back 00003 identity migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "users", false)

			if err := migrate.DownTo(t.Context(), stores.Writer(), migrate.SQLite, 1); err != nil {
				t.Fatalf("rolling back version 2 Go foundation migration: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), false)

			if err := migrate.Up(t.Context(), stores.Writer(), migrate.SQLite); err != nil {
				t.Fatalf("reapplying migrations through 00007 item additional-parts: %v", err)
			}
			assertGoMigrationMarker(t, stores.Writer(), true)
			assertSQLiteTable(t, stores.Writer(), "users", true)
			assertSQLiteTable(t, stores.Writer(), "items", true)
			assertSQLiteTable(t, stores.Writer(), "item_user_data_keys", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_lookup", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_user_data_keys_key", true)
			assertSQLiteTable(t, stores.Writer(), "item_additional_parts", true)
			assertSQLiteIndex(t, stores.Writer(), "ix_item_additional_parts_path", true)
			assertSQLiteAdditionalPartsCascade(t, stores.Writer())
		})
	}
}

func TestPragmasApplyToEveryConnection(t *testing.T) {
	stores := openStores(t, filepath.Join(t.TempDir(), "deadfin.db"))

	assertSQLitePragmas(t, stores.Writer())

	const readerConnections = 4
	ctx := t.Context()
	results := make(chan pragmaValues, readerConnections)
	release := make(chan struct{})
	var ready, completed sync.WaitGroup
	ready.Add(readerConnections)
	completed.Add(readerConnections)

	for range readerConnections {
		go func() {
			defer completed.Done()

			conn, err := stores.Reader().Conn(ctx)
			if err != nil {
				results <- pragmaValues{err: err}
				ready.Done()
				return
			}

			values := readPragmas(ctx, conn)
			ready.Done()
			<-release
			if err := conn.Close(); err != nil && values.err == nil {
				values.err = err
			}
			results <- values
		}()
	}

	ready.Wait()
	close(release)
	values := make([]pragmaValues, 0, readerConnections)
	for range readerConnections {
		values = append(values, <-results)
	}
	completed.Wait()
	for _, values := range values {
		assertReaderPragmaValues(t, values)
	}
}

func TestReaderRejectsMutationsWithoutChangingState(t *testing.T) {
	stores := openStores(t, filepath.Join(t.TempDir(), "deadfin.db"))
	writer := stores.Writer()
	if _, err := writer.ExecContext(t.Context(), `CREATE TABLE reader_mutation_contract (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("creating reader mutation contract table: %v", err)
	}

	if _, err := stores.Reader().ExecContext(t.Context(), `INSERT INTO reader_mutation_contract (id) VALUES (1)`); err == nil {
		t.Fatal("reader accepted a mutating statement")
	}

	var rows int
	if err := writer.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM reader_mutation_contract`).Scan(&rows); err != nil {
		t.Fatalf("counting rows after rejected reader mutation: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rejected reader mutation changed state: got %d rows, want 0", rows)
	}
}

func TestWriterSerializesWithoutBlockingReaders(t *testing.T) {
	stores := openStores(t, filepath.Join(t.TempDir(), "deadfin.db"))
	writer := stores.Writer()
	if got := writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
	if _, err := writer.ExecContext(t.Context(), `CREATE TABLE writer_contract (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("creating writer contract table: %v", err)
	}

	first, err := writer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning first writer transaction: %v", err)
	}
	defer func() { _ = first.Rollback() }()
	if _, err := first.ExecContext(t.Context(), `INSERT INTO writer_contract (id) VALUES (1)`); err != nil {
		t.Fatalf("writing first transaction: %v", err)
	}

	started := make(chan struct{})
	secondResult := make(chan error, 1)
	secondContext, cancelSecond := context.WithTimeout(t.Context(), time.Second)
	defer cancelSecond()
	go func() {
		close(started)
		second, err := writer.BeginTx(secondContext, nil)
		if err == nil {
			err = second.Rollback()
		}
		secondResult <- err
	}()
	<-started

	readerContext, cancelReader := context.WithTimeout(t.Context(), time.Second)
	defer cancelReader()
	var rows int
	if err := stores.Reader().QueryRowContext(readerContext, `SELECT COUNT(*) FROM writer_contract`).Scan(&rows); err != nil {
		t.Fatalf("reading while a writer is active: %v", err)
	}
	if rows != 0 {
		t.Fatalf("reader observed uncommitted data: got %d rows, want 0", rows)
	}

	select {
	case err := <-secondResult:
		t.Fatalf("second writer acquired the writer connection before first released it: %v", err)
	default:
	}

	if err := first.Rollback(); err != nil {
		t.Fatalf("releasing first writer transaction: %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second writer did not proceed after first released it: %v", err)
	}
}

func TestCloseIsIdempotentAndReleasesDatabaseHandles(t *testing.T) {
	stores := openStores(t, filepath.Join(t.TempDir(), "deadfin.db"))
	reader := stores.Reader()

	if err := stores.Close(); err != nil {
		t.Fatalf("closing stores: %v", err)
	}
	if err := stores.Close(); err != nil {
		t.Fatalf("closing stores a second time: %v", err)
	}
	if err := reader.PingContext(t.Context()); err == nil {
		t.Fatal("reader remained usable after Stores.Close")
	}
}

// TestIndependentSQLiteWritersReserveAtBegin verifies that the writer DSN
// opens transactions with BEGIN IMMEDIATE. A second store on the same file
// cannot begin a write transaction while the first holds the writer lock.
// With deferred mode, the second BeginTx succeeds and the lock contention
// surfaces only on the first write statement.
func TestIndependentSQLiteWritersReserveAtBegin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-writers.db")
	first := openStores(t, path)
	second := openStores(t, path)

	firstWriter := first.Writer()
	secondWriter := second.Writer()

	// The writer pool runs with a five-second busy timeout. Set the second
	// writer to zero so that BeginTx returns SQLITE_BUSY at once instead of
	// waiting for the first transaction to release the lock.
	if _, err := secondWriter.ExecContext(t.Context(), `PRAGMA busy_timeout=0`); err != nil {
		t.Fatalf("setting second writer busy_timeout to zero: %v", err)
	}

	firstTx, err := firstWriter.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning first writer transaction: %v", err)
	}
	defer func() { _ = firstTx.Rollback() }()

	if _, err := firstTx.ExecContext(t.Context(), `CREATE TABLE lock_contract (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("writing inside first transaction: %v", err)
	}

	if _, err := secondWriter.BeginTx(t.Context(), nil); err == nil {
		t.Fatal("second writer acquired a transaction while the first writer held the lock")
	} else if !isSQLiteBusy(err) {
		t.Fatalf("second writer BeginTx error = %v, want SQLITE_BUSY", err)
	}

	if err := firstTx.Rollback(); err != nil {
		t.Fatalf("releasing first writer transaction: %v", err)
	}

	secondTx, err := secondWriter.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning second writer transaction after lock release: %v", err)
	}
	if err := secondTx.Rollback(); err != nil {
		t.Fatalf("rolling back second writer transaction: %v", err)
	}
}

// TestWriterTxlockOverridesInputDSN verifies that the writer DSN overrides an
// _txlock value that the input DSN already carries. If the override is absent,
// an input _txlock=deferred survives alongside _txlock=immediate. The modernc
// driver reads the first value, so the second writer defers its transaction
// and BeginTx succeeds while the first writer holds the lock. With the
// override, the writer DSN carries one _txlock=immediate value, so the second
// writer reserves the lock at BeginTx and receives SQLITE_BUSY.
func TestWriterTxlockOverridesInputDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "txlock-override.db")
	dsn := "file:" + path + "?_txlock=deferred"

	first := openStores(t, dsn)
	second := openStores(t, dsn)

	firstWriter := first.Writer()
	secondWriter := second.Writer()

	// Set the second writer to a zero busy timeout so that BeginTx returns
	// SQLITE_BUSY at once instead of waiting for the first transaction.
	if _, err := secondWriter.ExecContext(t.Context(), `PRAGMA busy_timeout=0`); err != nil {
		t.Fatalf("setting second writer busy_timeout to zero: %v", err)
	}

	firstTx, err := firstWriter.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning first writer transaction: %v", err)
	}
	defer func() { _ = firstTx.Rollback() }()

	if _, err := firstTx.ExecContext(t.Context(), `CREATE TABLE lock_contract (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("writing inside first transaction: %v", err)
	}

	// In deferred mode, this BeginTx succeeds because the lock is not
	// reserved until the first write statement. The override keeps immediate
	// mode, so BeginTx reserves the lock and returns SQLITE_BUSY.
	if _, err := secondWriter.BeginTx(t.Context(), nil); err == nil {
		t.Fatal("second writer acquired a transaction while the first writer held the lock; input _txlock=deferred defeated the override")
	} else if !isSQLiteBusy(err) {
		t.Fatalf("second writer BeginTx error = %v, want SQLITE_BUSY", err)
	}

	if err := firstTx.Rollback(); err != nil {
		t.Fatalf("releasing first writer transaction: %v", err)
	}

	secondTx, err := secondWriter.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("beginning second writer transaction after lock release: %v", err)
	}
	if err := secondTx.Rollback(); err != nil {
		t.Fatalf("rolling back second writer transaction: %v", err)
	}
}

func isSQLiteBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") || strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}

func TestDialectOutputInvariants(t *testing.T) {
	stores := openStores(t, ":memory:")
	dialect := stores.Dialect()

	if got := dialect.Placeholder(1); got != "?" {
		t.Fatalf("first SQLite placeholder = %q, want ?", got)
	}
	if got := dialect.Placeholder(9); got != "?" {
		t.Fatalf("ninth SQLite placeholder = %q, want ?", got)
	}
	if got := dialect.Boolean(true); got != "1" {
		t.Fatalf("SQLite true literal = %q, want 1", got)
	}
	if got := dialect.Boolean(false); got != "0" {
		t.Fatalf("SQLite false literal = %q, want 0", got)
	}
	if got := dialect.Random(); got != "RANDOM()" {
		t.Fatalf("SQLite random expression = %q, want RANDOM()", got)
	}
	if got := dialect.NullsLast(); got != "NULLS LAST" {
		t.Fatalf("SQLite NULLS LAST fragment = %q", got)
	}
	if got := dialect.EscapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("SQLite LIKE escaping = %q", got)
	}
	if got := dialect.SearchClause("clean_name", "?"); got != `clean_name LIKE ? ESCAPE '\'` {
		t.Fatalf("SQLite search clause = %q", got)
	}
	if got := dialect.ContainsClause("clean_name", "?"); got != `instr(clean_name, ?) > 0` {
		t.Fatalf("SQLite contains clause = %q", got)
	}

	original := time.Date(2026, 7, 26, 15, 4, 5, 123456789, time.FixedZone("offset", -7*60*60))
	value := dialect.TimeValue(original)
	got, err := dialect.ScanTime(value)
	if err != nil {
		t.Fatalf("scanning dialect time value: %v", err)
	}
	want := original.UTC().Truncate(time.Microsecond)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("time round trip = %s (%s), want %s (UTC)", got, got.Location(), want)
	}
}

func openStores(t *testing.T, path string) store.Stores {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("opening SQLite stores: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("closing SQLite stores: %v", err)
		}
	})
	return stores
}

type pragmaValues struct {
	queryOnly        int
	journalMode      string
	journalSizeLimit int
	busyTimeout      int
	foreignKeys      int
	synchronous      int
	tempStore        int
	err              error
}

func assertSQLitePragmas(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("getting database connection: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing database connection: %v", err)
		}
	}()
	assertPragmaValues(t, readPragmas(t.Context(), conn))
}

func readPragmas(ctx context.Context, conn *sql.Conn) pragmaValues {
	var values pragmaValues
	values.err = conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&values.queryOnly)
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&values.journalMode)
	}
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA journal_size_limit").Scan(&values.journalSizeLimit)
	}
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&values.busyTimeout)
	}
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&values.foreignKeys)
	}
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&values.synchronous)
	}
	if values.err == nil {
		values.err = conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&values.tempStore)
	}
	return values
}

func assertPragmaValues(t *testing.T, values pragmaValues) {
	t.Helper()
	if values.err != nil {
		t.Fatalf("reading SQLite pragmas: %v", values.err)
	}
	if values.journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", values.journalMode)
	}
	if values.journalSizeLimit != 134217728 {
		t.Fatalf("journal_size_limit = %d, want 134217728", values.journalSizeLimit)
	}
	if values.busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", values.busyTimeout)
	}
	if values.foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", values.foreignKeys)
	}
	if values.synchronous != 1 {
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", values.synchronous)
	}
	if values.tempStore != 2 {
		t.Fatalf("temp_store = %d, want 2", values.tempStore)
	}
}

func assertReaderPragmaValues(t *testing.T, values pragmaValues) {
	t.Helper()
	assertPragmaValues(t, values)
	if values.queryOnly != 1 {
		t.Fatalf("query_only = %d, want 1", values.queryOnly)
	}
}

func assertGoMigrationMarker(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	var value string
	err := db.QueryRowContext(t.Context(), `SELECT value FROM store_metadata WHERE key = 'migration'`).Scan(&value)
	if want {
		if err != nil {
			t.Fatalf("reading Go migration marker: %v", err)
		}
		if value != "go-func" {
			t.Fatalf("Go migration marker = %q, want go-func", value)
		}
		return
	}
	if err != sql.ErrNoRows {
		t.Fatalf("Go migration marker remained after down migration: value=%q err=%v", value, err)
	}
}

func assertSQLiteTable(t *testing.T, db *sql.DB, table string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("checking SQLite table %q: %v", table, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("SQLite table %q exists = %t, want %t", table, got, want)
	}
}

func assertSQLiteIndex(t *testing.T, db *sql.DB, index string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
		t.Fatalf("checking SQLite index %q: %v", index, err)
	}
	if got := count == 1; got != want {
		t.Fatalf("SQLite index %q exists = %t, want %t", index, got, want)
	}
}

func assertSQLiteAdditionalPartsCascade(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `PRAGMA foreign_key_list(item_additional_parts)`)
	if err != nil {
		t.Fatalf("listing item_additional_parts foreign keys: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close item_additional_parts foreign key rows: %v", err)
		}
	}()
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scanning item_additional_parts foreign key: %v", err)
		}
		if table == "items" && from == "item_id" && to == "id" && onDelete == "CASCADE" {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate item_additional_parts foreign keys: %v", err)
	}
	t.Fatal("item_additional_parts.item_id foreign key to items.id with ON DELETE CASCADE is missing")
}
