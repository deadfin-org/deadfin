// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
)

// TestItemDetailRoutesUseTheStockDetailedFieldSetButItemsRemainCallerSelected
// protects the essential route distinction. The item-by-ID endpoints build a
// detailed BaseItemDto even when the caller requests a small field set, whereas
// /Items continues to project precisely the caller-selected field set.
func TestItemDetailRoutesUseTheStockDetailedFieldSetButItemsRemainCallerSelected(t *testing.T) {
	userID, itemID := itemsHandlerUUID(t, 970), itemsHandlerUUID(t, 971)
	reader := &itemsHandlerReader{result: store.ItemQueryResult{Items: []item.Item{{ID: itemID, Kind: item.KindMovie}}}}
	builder := &itemsHandlerBuilder{items: []dto.BaseItemDto{{ID: itemID}}}
	routes := router.New()
	handlers.RegisterItems(routes, reader, builder, clock.NewReal())

	requested := []string{"Path", "MediaSources", "MediaStreams"}
	wantDetailFields := append([]string(nil), stockDetailDefaultFields...)
	for _, path := range []string{
		"/Items/" + itemID.Dashed() + "?userId=" + userID.Dashed() + "&Fields=Path,MediaSources,MediaStreams",
		"/Users/" + userID.Dashed() + "/Items/" + itemID.Dashed() + "?userId=" + itemsHandlerUUID(t, 972).Dashed() + "&Fields=Path,MediaSources,MediaStreams",
	} {
		record := httptest.NewRecorder()
		routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, path, nil))
		if record.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", path, record.Code, record.Body.String())
		}
	}
	if got, want := len(builder.options), 2; got != want {
		t.Fatalf("detail build calls = %d, want %d", got, want)
	}
	for index, options := range builder.options {
		if !reflect.DeepEqual(options.Fields, wantDetailFields) {
			t.Errorf("detail route %d Fields = %#v, want stock defaults plus caller-requested fields %#v", index, options.Fields, wantDetailFields)
		}
		if !options.EnableUserData {
			t.Errorf("detail route %d disabled default user-data projection", index)
		}
	}

	record := httptest.NewRecorder()
	routes.ServeHTTP(record, httptest.NewRequest(http.MethodGet, "/Items?userId="+userID.Dashed()+"&Fields=Path,MediaSources,MediaStreams", nil))
	if record.Code != http.StatusOK {
		t.Fatalf("GET /Items status = %d, want 200: %s", record.Code, record.Body.String())
	}
	if got, want := builder.options[2].Fields, requested; !reflect.DeepEqual(got, want) {
		t.Fatalf("/Items Fields = %#v, want caller-selected %#v; detail defaults must not leak into list projection", got, want)
	}
}

// TestItemDetailRoutesRenderStockDetailedMovieAndFolder crosses modern and
// legacy item-by-ID paths with the real store and DTO builder. It pins the
// meaningful stock distinction: detail responses retain their default empty
// arrays/maps, access/count/user facts, and deterministic display-preference
// identity; a list response for the same rows stays sparse.
func TestItemDetailRoutesRenderStockDetailedMovieAndFolder(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open detail projection SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := stores.Close(); err != nil {
			t.Errorf("close detail projection SQLite store: %v", err)
		}
	})
	items := store.NewItemStore(stores)
	userID, folderID, movieID := itemsHandlerUUID(t, 980), itemsHandlerUUID(t, 981), itemsHandlerUUID(t, 982)
	folderName, movieName, path, container := "Detailed folder", "Detailed movie", "/library/detailed/movie.mkv", "mkv"
	created := time.Date(2026, time.July, 27, 17, 55, 42, 0, time.UTC)
	year, width, height := 2021, 320, 180
	ticks := int64(10_210_000)
	folder := item.Item{ID: folderID, Kind: item.KindFolder, IsFolder: true, Name: &folderName, DateCreated: &created}
	movie := item.Item{
		ID: movieID, Kind: item.KindMovie, IsMovie: true, ParentID: &folderID, Name: &movieName,
		Path: &path, Container: &container, DateCreated: &created, ProductionYear: &year, Width: &width, Height: &height, RunTimeTicks: &ticks,
		Values: item.ItemValues{Genres: []string{}, Tags: []string{}},
	}
	for _, value := range []item.Item{folder, movie} {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert detailed %s fixture: %v", value.Kind, err)
		}
	}
	if err := items.ReplaceStreams(t.Context(), movieID, []playback.MediaStream{{Index: 0, Type: playback.StreamTypeVideo, Codec: "mpeg4", Width: &width, Height: &height}}); err != nil {
		t.Fatalf("seed detailed movie streams: %v", err)
	}
	if err := items.UpsertUserData(t.Context(), store.UserData{UserID: userID, CustomKey: movieID.String(), ItemID: movieID, Played: false, PlayCount: 0, IsFavorite: false}); err != nil {
		t.Fatalf("seed detailed movie user data: %v", err)
	}

	routes := router.New()
	handlers.RegisterItems(routes, items, dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil), "detail-server"), clock.NewReal())
	for _, tc := range []struct {
		name, path, accept string
		id                 dto.UUID
		camel              bool
	}{
		{name: "modern movie Swiftfin shape", path: "/Items/" + movieID.Dashed() + "?userId=" + userID.Dashed(), id: movieID},
		{name: "legacy movie camel", path: "/Users/" + userID.Dashed() + "/Items/" + movieID.Dashed() + "?userId=" + itemsHandlerUUID(t, 983).Dashed(), accept: `application/json; profile="CamelCase"`, id: movieID, camel: true},
		{name: "modern folder", path: "/Items/" + folderID.Dashed() + "?userId=" + userID.Dashed(), id: folderID},
		{name: "legacy folder camel", path: "/Users/" + userID.Dashed() + "/Items/" + folderID.Dashed(), accept: `application/json; profile="CamelCase"`, id: folderID, camel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.Header.Set("Accept", tc.accept)
			routes.ServeHTTP(record, request)
			if record.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200: %s", tc.path, record.Code, record.Body.String())
			}
			row := detailProjectionObject(t, record.Body.Bytes())
			detailProjectionAssertStockShape(t, row, tc.id, tc.camel, tc.id == movieID)
		})
	}

	list := httptest.NewRecorder()
	routes.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/Items?userId="+userID.Dashed()+"&ids="+movieID.Dashed()+","+folderID.Dashed()+"&Fields=Path,MediaSources,MediaStreams", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("GET sparse /Items status = %d, want 200: %s", list.Code, list.Body.String())
	}
	var page struct{ Items []map[string]json.RawMessage }
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode sparse /Items response: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("sparse /Items rows = %d, want 2", len(page.Items))
	}
	for _, row := range page.Items {
		for _, defaultOnly := range []string{"CanDelete", "CanDownload", "DisplayPreferencesId", "PlayAccess", "LockData", "LockedFields", "Chapters", "Trickplay"} {
			if value, found := row[defaultOnly]; found {
				t.Errorf("/Items unexpectedly inherited detail-only %s=%s", defaultOnly, value)
			}
		}
	}
}

var stockDetailDefaultFields = []string{
	"AirTime", "CanDelete", "CanDownload", "ChannelInfo", "Chapters", "Trickplay", "ChildCount", "CumulativeRunTimeTicks", "CustomRating", "DateCreated", "DateLastMediaAdded", "DisplayPreferencesId", "Etag", "ExternalUrls", "Genres", "ItemCounts", "MediaSourceCount", "MediaSources", "OriginalTitle", "Overview", "ParentId", "Path", "People", "PlayAccess", "ProductionLocations", "ProviderIds", "PrimaryImageAspectRatio", "RecursiveItemCount", "Settings", "SeriesStudio", "SortName", "SpecialEpisodeNumbers", "Studios", "Taglines", "Tags", "RemoteTrailers", "MediaStreams", "DateLastRefreshed", "DateLastSaved", "ChannelImage", "EnableMediaSourceDisplay", "Width", "Height", "ExtraIds", "LocalTrailerCount", "IsHD", "SpecialFeatureCount",
}

func detailProjectionObject(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var row map[string]json.RawMessage
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode detailed item response: %v; body=%s", err, body)
	}
	return row
}

func detailProjectionAssertStockShape(t *testing.T, row map[string]json.RawMessage, id dto.UUID, camel, movie bool) {
	t.Helper()
	key := func(pascal, camelCase string) string {
		if camel {
			return camelCase
		}
		return pascal
	}
	present := []string{
		key("BackdropImageTags", "backdropImageTags"), key("CanDelete", "canDelete"), key("CanDownload", "canDownload"), key("Chapters", "chapters"), key("DateCreated", "dateCreated"), key("DisplayPreferencesId", "displayPreferencesId"), key("EnableMediaSourceDisplay", "enableMediaSourceDisplay"), key("ExternalUrls", "externalUrls"), key("GenreItems", "genreItems"), key("Genres", "genres"), key("ImageBlurHashes", "imageBlurHashes"), key("ImageTags", "imageTags"), key("IsFolder", "isFolder"), key("LocalTrailerCount", "localTrailerCount"), key("LockData", "lockData"), key("LockedFields", "lockedFields"), key("People", "people"), key("PlayAccess", "playAccess"), key("ProductionLocations", "productionLocations"), key("ProviderIds", "providerIds"), key("RemoteTrailers", "remoteTrailers"), key("SpecialFeatureCount", "specialFeatureCount"), key("Studios", "studios"), key("Taglines", "taglines"), key("Tags", "tags"), key("Trickplay", "trickplay"), key("UserData", "userData"),
	}
	if movie {
		present = append(present, key("Path", "path"), key("MediaSources", "mediaSources"), key("MediaStreams", "mediaStreams"))
	} else {
		for _, field := range []string{key("Path", "path"), key("MediaSources", "mediaSources"), key("MediaStreams", "mediaStreams")} {
			if value, found := row[field]; found {
				t.Errorf("detailed folder %s unexpectedly emitted unrequested %s=%s", id, field, value)
			}
		}
	}
	for _, field := range present {
		if value, found := row[field]; !found {
			t.Errorf("detailed item %s omitted stock default %s; response keys=%v", id, field, detailProjectionKeys(row))
		} else if value == nil {
			t.Errorf("detailed item %s emitted nil JSON bytes for %s", id, field)
		}
	}
	for _, absent := range []string{key("AirDays", "airDays"), key("AlbumCount", "albumCount"), key("CommunityRating", "communityRating"), key("CurrentProgram", "currentProgram"), key("IsPlaceHolder", "isPlaceHolder")} {
		if value, found := row[absent]; found {
			t.Errorf("detailed item %s unexpectedly emitted absent stock field %s=%s", id, absent, value)
		}
	}

	for field, want := range map[string]string{
		key("BackdropImageTags", "backdropImageTags"):     "[]",
		key("Chapters", "chapters"):                       "[]",
		key("ExternalUrls", "externalUrls"):               "[]",
		key("GenreItems", "genreItems"):                   "[]",
		key("Genres", "genres"):                           "[]",
		key("ImageBlurHashes", "imageBlurHashes"):         "{}",
		key("ImageTags", "imageTags"):                     "{}",
		key("LockedFields", "lockedFields"):               "[]",
		key("People", "people"):                           "[]",
		key("ProductionLocations", "productionLocations"): "[]",
		key("ProviderIds", "providerIds"):                 "{}",
		key("RemoteTrailers", "remoteTrailers"):           "[]",
		key("Studios", "studios"):                         "[]",
		key("Taglines", "taglines"):                       "[]",
		key("Tags", "tags"):                               "[]",
		key("Trickplay", "trickplay"):                     "{}",
	} {
		if got := string(row[field]); got != want {
			t.Errorf("detailed item %s %s = %s, want stock empty %s", id, field, got, want)
		}
	}
	for field, want := range map[string]string{
		key("CanDelete", "canDelete"):                               "true",
		key("EnableMediaSourceDisplay", "enableMediaSourceDisplay"): "true",
		key("LocalTrailerCount", "localTrailerCount"):               "0",
		key("LockData", "lockData"):                                 "false",
		key("PlayAccess", "playAccess"):                             `"Full"`,
		key("SpecialFeatureCount", "specialFeatureCount"):           "0",
	} {
		if got := string(row[field]); got != want {
			t.Errorf("detailed item %s %s = %s, want %s", id, field, got, want)
		}
	}
	canDownload := key("CanDownload", "canDownload")
	wantDownload := "false"
	if movie {
		wantDownload = "true"
	}
	if got := string(row[canDownload]); got != wantDownload {
		t.Errorf("detailed item %s %s = %s, want %s", id, canDownload, got, wantDownload)
	}
	if movie {
		for _, field := range []string{key("ChildCount", "childCount"), key("RecursiveItemCount", "recursiveItemCount")} {
			if value, found := row[field]; found {
				t.Errorf("detailed movie %s unexpectedly emitted folder-only %s=%s", id, field, value)
			}
		}
	} else {
		for field, want := range map[string]string{key("ChildCount", "childCount"): "1", key("RecursiveItemCount", "recursiveItemCount"): "1"} {
			if got := string(row[field]); got != want {
				t.Errorf("detailed folder %s %s = %s, want %s", id, field, got, want)
			}
		}
	}
	preferenceID := key("DisplayPreferencesId", "displayPreferencesId")
	wantPreference := id.String()
	if movie {
		wantPreference = "dbf7709c41faaa746463d67978eb863d"
	}
	if got := string(row[preferenceID]); got != `"`+wantPreference+`"` {
		t.Errorf("detailed item %s %s = %s, want stock identity %q", id, preferenceID, got, wantPreference)
	}
	userData := key("UserData", "userData")
	var data map[string]json.RawMessage
	if err := json.Unmarshal(row[userData], &data); err != nil {
		t.Errorf("detailed item %s UserData = %s, want object: %v", id, row[userData], err)
	} else if got := string(data[key("ItemId", "itemId")]); got != `"`+id.String()+`"` {
		t.Errorf("detailed item %s UserData ItemId = %s, want item identity", id, got)
	}
}

func detailProjectionKeys(row map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	return keys
}
