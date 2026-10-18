// SPDX-License-Identifier: GPL-2.0-only

package jellyfinsource_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store/jellyfinsource"

	_ "modernc.org/sqlite"
)

// fixture builds a jellyfin.db with the columns the source reads, in the
// shapes Jellyfin 10.11 writes: uppercase TEXT GUIDs and EF TEXT dates.
func fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jellyfin.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE Users (Id TEXT PRIMARY KEY, Username TEXT, Password TEXT, AuthenticationProviderId TEXT, PasswordResetProviderId TEXT,
			AudioLanguagePreference TEXT, SubtitleLanguagePreference TEXT, SubtitleMode INTEGER, CastReceiverId TEXT,
			DisplayCollectionsView INTEGER, DisplayMissingEpisodes INTEGER, EnableLocalPassword INTEGER, EnableNextEpisodeAutoPlay INTEGER,
			EnableUserPreferenceAccess INTEGER, HidePlayedInLatest INTEGER, PlayDefaultAudioTrack INTEGER, RememberAudioSelections INTEGER,
			RememberSubtitleSelections INTEGER, InvalidLoginAttemptCount INTEGER, LoginAttemptsBeforeLockout INTEGER, MaxActiveSessions INTEGER,
			MaxParentalRatingScore INTEGER, MaxParentalRatingSubScore INTEGER, RemoteClientBitrateLimit INTEGER, SyncPlayAccess INTEGER,
			LastLoginDate TEXT, LastActivityDate TEXT, InternalId INTEGER)`,
		`CREATE TABLE Permissions (Id INTEGER PRIMARY KEY, Kind INTEGER, UserId TEXT, Value INTEGER)`,
		`CREATE TABLE Preferences (Id INTEGER PRIMARY KEY, Kind INTEGER, UserId TEXT, Value TEXT)`,
		`CREATE TABLE Devices (Id INTEGER PRIMARY KEY, UserId TEXT, AccessToken TEXT, AppName TEXT, AppVersion TEXT, DeviceName TEXT, DeviceId TEXT,
			IsActive INTEGER, DateCreated TEXT, DateModified TEXT, DateLastActivity TEXT)`,
		`CREATE TABLE ApiKeys (Id INTEGER PRIMARY KEY, Name TEXT, AccessToken TEXT, DateCreated TEXT, DateLastActivity TEXT)`,
		`CREATE TABLE BaseItems (Id TEXT PRIMARY KEY, Type TEXT, Name TEXT, Path TEXT)`,
		`CREATE TABLE UserData (ItemId TEXT, UserId TEXT, CustomDataKey TEXT, AudioStreamIndex INTEGER, IsFavorite INTEGER, LastPlayedDate TEXT,
			Likes INTEGER, PlayCount INTEGER, PlaybackPositionTicks INTEGER, Played INTEGER, Rating REAL, SubtitleStreamIndex INTEGER)`,
		`INSERT INTO Users VALUES ('4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','whiskey','$PBKDF2-SHA512$iterations=210000$AA$BB','Default','Reset',
			'jpn','eng',4,'',1,0,0,1,1,1,1,1,1,0,NULL,0,NULL,NULL,NULL,2,'2026-10-09 06:37:31.7425077',NULL,1)`,
		`INSERT INTO Permissions (Kind, UserId, Value) VALUES (0,'4E703B0E-6E78-4EA6-9C48-43A2654C5CB1',1),(16,'4E703B0E-6E78-4EA6-9C48-43A2654C5CB1',0)`,
		`INSERT INTO Preferences (Kind, UserId, Value) VALUES (5,'4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','f137a2dd-21bb-c1b9-9aa5-c0f6bf02a805')`,
		`INSERT INTO Devices (UserId, AccessToken, AppName, AppVersion, DeviceName, DeviceId, IsActive, DateCreated, DateModified, DateLastActivity)
			VALUES ('4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','token-1','Jellyfin tvOS','1.0','AppleTV','tv-1',0,'2025-01-01 00:00:00','2025-01-01 00:00:00','2026-10-09 06:37:31.7425077')`,
		`INSERT INTO ApiKeys (Name, AccessToken, DateCreated, DateLastActivity) VALUES ('Jellyseerr','key-1','2024-01-01 00:00:00','2026-10-09 18:25:06.2571647')`,
		`INSERT INTO BaseItems VALUES ('F137A2DD-21BB-C1B9-9AA5-C0F6BF02A805','MediaBrowser.Controller.Entities.CollectionFolder','Movies',NULL),
			('00000000-0000-0000-0000-000000000001','PLACEHOLDER',NULL,NULL),
			('AAAAAAAA-0000-0000-0000-000000000001','MediaBrowser.Controller.Entities.Movies.Movie','Heat','/media/movies/Heat (1995)/Heat.mkv')`,
		`INSERT INTO UserData VALUES ('AAAAAAAA-0000-0000-0000-000000000001','4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','949',1,1,'2024-10-10 04:14:32.1774009',1,3,600000000,1,7.5,NULL),
			('00000000-0000-0000-0000-000000000001','4E703B0E-6E78-4EA6-9C48-43A2654C5CB1','tt0113277',NULL,0,NULL,NULL,1,0,1,NULL,NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	return path
}

func TestSourceReadsJellyfinDatabase(t *testing.T) {
	src, err := jellyfinsource.Open(t.Context(), fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	userID, _ := dto.ParseUUID("4e703b0e6e784ea69c4843a2654c5cb1")

	users, err := src.Users(t.Context())
	if err != nil || len(users) != 1 {
		t.Fatalf("Users = %+v, %v", users, err)
	}
	user := users[0]
	if user.ID != userID || user.Username != "whiskey" || user.PasswordHash != "$PBKDF2-SHA512$iterations=210000$AA$BB" {
		t.Errorf("user = %+v", user)
	}
	if user.SubtitleMode != 4 || user.SyncPlayAccess != 2 || user.AudioLanguagePreference != "jpn" || user.LoginAttemptsBeforeLockout != nil {
		t.Errorf("user settings = %+v", user)
	}
	if want := time.Date(2026, 10, 9, 6, 37, 31, 742507700, time.UTC); user.LastLoginDate == nil || !user.LastLoginDate.Equal(want) {
		t.Errorf("LastLoginDate = %v, want %v", user.LastLoginDate, want)
	}
	if !user.Permissions[0] || user.Permissions[16] || user.Preferences[5] != "f137a2dd-21bb-c1b9-9aa5-c0f6bf02a805" {
		t.Errorf("permissions = %v, preferences = %v", user.Permissions, user.Preferences)
	}

	// Jellyfin leaves IsActive at 0 for every device; all of them are read.
	devices, err := src.Devices(t.Context())
	if err != nil || len(devices) != 1 || devices[0].AccessToken != "token-1" || devices[0].UserID != userID {
		t.Errorf("Devices = %+v, %v", devices, err)
	}
	keys, err := src.APIKeys(t.Context())
	if err != nil || len(keys) != 1 || keys[0].Name != "Jellyseerr" || keys[0].AccessToken != "key-1" {
		t.Errorf("APIKeys = %+v, %v", keys, err)
	}

	data, err := src.UserData(t.Context())
	if err != nil || len(data) != 2 {
		t.Fatalf("UserData = %+v, %v", data, err)
	}
	live, orphan := data[0], data[1]
	if live.CustomDataKey != "949" || live.Orphaned() || live.ItemPath != "/media/movies/Heat (1995)/Heat.mkv" || !live.Played ||
		live.PlayCount != 3 || live.PlaybackPositionTicks != 600000000 || !live.IsFavorite || live.Rating == nil || *live.Rating != 7.5 ||
		live.Likes == nil || !*live.Likes || live.AudioStreamIndex == nil || *live.AudioStreamIndex != 1 || live.LastPlayedDate == nil {
		t.Errorf("live row = %+v", live)
	}
	if !orphan.Orphaned() || orphan.ItemPath != "" || orphan.ItemType != "" || orphan.CustomDataKey != "tt0113277" {
		t.Errorf("orphaned row = %+v", orphan)
	}

	libraries, err := src.Libraries(t.Context())
	if err != nil || len(libraries) != 1 || libraries[0].Name != "Movies" {
		t.Errorf("Libraries = %+v, %v", libraries, err)
	}
}

func TestSourceOpensReadOnly(t *testing.T) {
	path := fixture(t)
	src, err := jellyfinsource.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Users(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The source holds no write path; prove the file is unchanged by reading it
	// again through a separate handle.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM Users`).Scan(&count); err != nil || count != 1 {
		t.Errorf("users after read = %d, %v", count, err)
	}
}

// A WAL-mode database copied with Jellyfin stopped must open from a
// read-only directory, as the import guide mounts it (deadfin#697).
func TestSourceOpensWALDatabaseInReadOnlyDirectory(t *testing.T) {
	path := fixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	src, err := jellyfinsource.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer src.Close()
	users, err := src.Users(t.Context())
	if err != nil || len(users) != 1 {
		t.Fatalf("Users = %d, %v", len(users), err)
	}
	for _, suffix := range []string{"-shm", "-wal"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Errorf("%s exists after a read-only open: %v", suffix, err)
		}
	}
}

// A non-empty -wal file means the copy was taken while Jellyfin ran; an
// immutable open would miss its committed changes.
func TestSourceRefusesNonEmptyWAL(t *testing.T) {
	path := fixture(t)
	if err := os.WriteFile(path+"-wal", []byte("not empty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if src, err := jellyfinsource.Open(t.Context(), path); err == nil {
		_ = src.Close()
		t.Fatal("Open with a non-empty -wal succeeded")
	}
}
