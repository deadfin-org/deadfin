// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/imageserve"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/path"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// namedEntityFixture seeds a SQLite store with one entity of each by-name kind
// and one movie that credits them.
type namedEntityFixture struct {
	items                       *store.ItemStore
	person, genre, studio       item.Item
	artist, albumArtist, credit item.Item
}

func newNamedEntityFixture(t *testing.T) namedEntityFixture {
	t.Helper()
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open named entity store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close named entity store: %v", err)
		}
	})
	fixture := namedEntityFixture{items: store.NewItemStore(stores)}
	fixture.person = library.PersonEntity("Ada Lovelace")
	fixture.person.SortName = fixture.person.Name
	fixture.person.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/ada.jpg"}}
	fixture.genre = library.TaxonomyEntity(item.KindGenre, "Sci-Fi & Fantasy")
	fixture.genre.SortName = fixture.genre.Name
	fixture.studio = library.TaxonomyEntity(item.KindStudio, "AC/DC Films")
	fixture.studio.SortName = fixture.studio.Name
	fixture.artist = item.Item{ID: itemNamesUUID(t, 901), Kind: item.KindMusicArtist, Name: itemNamesString("Track Artist"), SortName: itemNamesString("Track Artist")}
	fixture.albumArtist = item.Item{ID: itemNamesUUID(t, 902), Kind: item.KindMusicArtist, Name: itemNamesString("Album Artist"), SortName: itemNamesString("Album Artist")}
	fixture.credit = item.Item{ID: itemNamesUUID(t, 903), Kind: item.KindMusicAlbum, Name: itemNamesString("Album"), SortName: itemNamesString("Album"), Values: item.ItemValues{Artists: []string{"Track Artist"}, AlbumArtists: []string{"Album Artist"}}}
	for _, value := range []item.Item{fixture.person, fixture.genre, fixture.studio, fixture.artist, fixture.albumArtist, fixture.credit} {
		if err := fixture.items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("seed %s: %v", value.ID, err)
		}
	}
	return fixture
}

func TestNamedEntityLookupRoutes(t *testing.T) {
	fixture := newNamedEntityFixture(t)
	rt := router.New()
	handlers.RegisterItemNames(rt, fixture.items, &itemNamesBuilder{}, clock.NewReal())

	for _, tc := range []struct {
		name, path string
		wantStatus int
		wantID     dto.UUID
		wantKind   string
	}{
		{name: "person", path: "/Persons/Ada%20Lovelace", wantStatus: http.StatusOK, wantID: fixture.person.ID, wantKind: "Person"},
		{name: "person case-insensitive", path: "/Persons/ada%20lovelace", wantStatus: http.StatusOK, wantID: fixture.person.ID, wantKind: "Person"},
		{name: "unknown person", path: "/Persons/Nobody", wantStatus: http.StatusNotFound},
		{name: "genre", path: "/Genres/Sci-Fi%20%26%20Fantasy", wantStatus: http.StatusOK, wantID: fixture.genre.ID, wantKind: "Genre"},
		{name: "genre slug", path: "/Genres/Sci-Fi-Fantasy", wantStatus: http.StatusOK, wantID: fixture.genre.ID, wantKind: "Genre"},
		{name: "unknown genre", path: "/Genres/Western", wantStatus: http.StatusOK, wantID: library.TaxonomyEntity(item.KindGenre, "Western").ID, wantKind: "Genre"},
		{name: "unknown music genre", path: "/MusicGenres/Jazz", wantStatus: http.StatusOK, wantID: library.TaxonomyEntity(item.KindMusicGenre, "Jazz").ID, wantKind: "MusicGenre"},
		{name: "studio with escaped slash", path: "/Studios/AC%2FDC%20Films", wantStatus: http.StatusOK, wantID: fixture.studio.ID, wantKind: "Studio"},
		{name: "artist", path: "/Artists/Track%20Artist", wantStatus: http.StatusOK, wantID: fixture.artist.ID, wantKind: "MusicArtist"},
		{name: "unknown artist", path: "/Artists/Nobody", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d: %s", tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var got dto.BaseItemDto
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s: %v", tc.path, err)
			}
			if got.ID != tc.wantID || string(got.Type) != tc.wantKind {
				t.Fatalf("GET %s = %s %s, want %s %s", tc.path, got.Type, got.ID, tc.wantKind, tc.wantID)
			}
		})
	}

	// The by-name read of an unknown genre must not create the entity.
	result, err := fixture.items.Query(t.Context(), store.ItemQuery{IncludeItemTypes: []item.Kind{item.KindGenre, item.KindMusicGenre}})
	if err != nil {
		t.Fatalf("query genres: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("stored genres = %d, want only the seeded genre", len(result.Items))
	}
}

func TestAlbumArtistsListsOnlyAlbumArtists(t *testing.T) {
	fixture := newNamedEntityFixture(t)
	rt := router.New()
	handlers.RegisterItemNames(rt, fixture.items, &itemNamesBuilder{}, clock.NewReal())

	names := func(path string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
		var page dto.QueryResult[dto.BaseItemDto]
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		out := make([]string, len(page.Items))
		for index := range page.Items {
			out[index] = page.Items[index].Name
		}
		return out
	}
	if got := names("/Artists/AlbumArtists?recursive=true"); !reflect.DeepEqual(got, []string{"Album Artist"}) {
		t.Fatalf("album artists = %#v, want only Album Artist", got)
	}
	if got := names("/Artists?recursive=true"); !reflect.DeepEqual(got, []string{"Album Artist", "Track Artist"}) {
		t.Fatalf("artists = %#v, want both artists", got)
	}
}

func TestNamedEntityImageRoutesServeEntityImage(t *testing.T) {
	fixture := newNamedEntityFixture(t)
	artifact := writeArtifact(t, "JPEG-BYTES")
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/jpeg"}}
	rt := router.New()
	handlers.RegisterImages(rt, fixture.items, renderer, path.VirtualPathExpander{}, nil, nil)

	for _, tc := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/Persons/Ada%20Lovelace/Images/Primary", wantStatus: http.StatusOK},
		{path: "/Persons/Ada%20Lovelace/Images/Primary/0?maxWidth=120", wantStatus: http.StatusOK},
		{path: "/Persons/Ada%20Lovelace/Images/Backdrop", wantStatus: http.StatusNotFound},
		{path: "/Persons/Nobody/Images/Primary", wantStatus: http.StatusNotFound},
		{path: "/Genres/Sci-Fi-Fantasy/Images/Primary", wantStatus: http.StatusNotFound},
		{path: "/Artists/Track%20Artist/Images/Primary/0", wantStatus: http.StatusNotFound},
		{path: "/Studios/AC%2FDC%20Films/Images/Bogus", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			renderer.called = false
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d: %s", tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if !renderer.called || renderer.gotInfo.Path != fixture.person.ImageInfos[0].Path {
				t.Fatalf("rendered %#v, want the person's Primary image", renderer.gotInfo)
			}
			if got := rec.Header().Get("ETag"); got != `"ada.jpg"` {
				t.Fatalf("ETag = %q, want the stored image tag", got)
			}
		})
	}
}
