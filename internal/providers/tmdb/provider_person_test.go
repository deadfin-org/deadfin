// SPDX-License-Identifier: GPL-2.0-only

package tmdb_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
	"github.com/deadfin-org/deadfin/internal/providers/remote/remotetest"
	"github.com/deadfin-org/deadfin/internal/providers/tmdb"
)

const personCassette = "testdata/person.json"

func personItem(name, tmdbID string) item.Item {
	target := item.Item{Kind: item.KindPerson, Name: stringPtr(name)}
	if tmdbID != "" {
		target.ProviderIDs.Set("Tmdb", tmdbID)
	}
	return target
}

func newPersonProvider(t *testing.T) (*tmdb.Provider, *recordingDoer) {
	t.Helper()
	cassette, err := remotetest.Load(personCassette)
	if err != nil {
		t.Fatalf("load cassette: %v", err)
	}
	doer := &recordingDoer{inner: cassette}
	return newTVProvider(t, doer, nil), doer
}

func fullPersonRefresh() providers.PersonRefresh {
	return providers.PersonRefresh{Language: "en", Country: "US", Metadata: true, Images: true}
}

// TestRefreshPersonMapsTMDbPerson ports TmdbPersonProvider.GetMetadata and
// TmdbPersonImageProvider.GetImages for a person with a TMDb id.
func TestRefreshPersonMapsTMDbPerson(t *testing.T) {
	provider, doer := newPersonProvider(t)
	target := personItem("Jake Gyllenhaal", "131")

	handled, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &target)
	if err != nil || !handled {
		t.Fatalf("RefreshPerson = %v, %v; want handled", handled, err)
	}
	if target.Name == nil || *target.Name != "Jake Gyllenhaal" {
		t.Errorf("Name = %v, want the incoming name", target.Name)
	}
	if target.Overview == nil || *target.Overview != "An American actor." {
		t.Errorf("Overview = %v, want the biography", target.Overview)
	}
	if want := time.Date(1980, 12, 19, 0, 0, 0, 0, time.UTC); target.PremiereDate == nil || !target.PremiereDate.Equal(want) {
		t.Errorf("PremiereDate = %v, want %v (birthday)", target.PremiereDate, want)
	}
	if target.EndDate != nil {
		t.Errorf("EndDate = %v, want nil for a null deathday", target.EndDate)
	}
	if want := []string{"Los Angeles, California, USA"}; !reflect.DeepEqual(target.Values.ProductionLocations, want) {
		t.Errorf("ProductionLocations = %v, want %v (place_of_birth)", target.Values.ProductionLocations, want)
	}
	if got, _ := target.ProviderIDs.Get("Imdb"); got != "nm0350453" {
		t.Errorf("Imdb id = %q, want nm0350453", got)
	}
	want := []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/jake-profile.jpg", Width: 1000, Height: 1500}}
	if !reflect.DeepEqual(target.ImageInfos, want) {
		t.Errorf("ImageInfos = %#v, want the first profile as Primary", target.ImageInfos)
	}
	if paths := doer.apiPaths(); !reflect.DeepEqual(paths, []string{"/3/person/131"}) {
		t.Errorf("requests = %v, want one person request", paths)
	}
}

// TestRefreshPersonSearchesWithoutID ports the GetMetadata fallback: without
// a TMDb id the first /search/person result supplies it.
func TestRefreshPersonSearchesWithoutID(t *testing.T) {
	provider, doer := newPersonProvider(t)
	target := personItem("Jake Gyllenhaal", "")

	if handled, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &target); err != nil || !handled {
		t.Fatalf("RefreshPerson = %v, %v; want handled", handled, err)
	}
	if got, _ := target.ProviderIDs.Get("Tmdb"); got != "131" {
		t.Errorf("Tmdb id = %q, want 131 from the first search result", got)
	}
	if paths := doer.apiPaths(); !reflect.DeepEqual(paths, []string{"/3/search/person", "/3/person/131"}) {
		t.Errorf("requests = %v, want search then person", paths)
	}
}

// TestRefreshPersonImagesNeedTMDbID ports TmdbPersonImageProvider: without a
// TMDb id the image provider returns nothing and does not search.
func TestRefreshPersonImagesNeedTMDbID(t *testing.T) {
	provider, doer := newPersonProvider(t)
	target := personItem("Jake Gyllenhaal", "")
	request := fullPersonRefresh()
	request.Metadata = false

	if handled, err := provider.RefreshPerson(t.Context(), request, &target); err != nil || handled {
		t.Fatalf("RefreshPerson = %v, %v; want unhandled", handled, err)
	}
	if len(doer.apiPaths()) != 0 || len(target.ImageInfos) != 0 {
		t.Errorf("requests = %v, images = %v; want none", doer.apiPaths(), target.ImageInfos)
	}

	target = personItem("Jake Gyllenhaal", "131")
	if handled, err := provider.RefreshPerson(t.Context(), request, &target); err != nil || !handled {
		t.Fatalf("RefreshPerson = %v, %v; want handled", handled, err)
	}
	if target.Overview != nil || len(target.ImageInfos) != 1 {
		t.Errorf("Overview = %v, images = %v; want only the Primary image", target.Overview, target.ImageInfos)
	}
}

// TestRefreshPersonMergesLikeMergeBaseItemData checks the merge into stored
// values: fill empty fields, keep locked fields, add a new production
// location, keep a stored provider id, and keep a stored Primary image.
func TestRefreshPersonMergesLikeMergeBaseItemData(t *testing.T) {
	provider, _ := newPersonProvider(t)
	birthday := time.Date(1981, 1, 1, 0, 0, 0, 0, time.UTC)
	target := personItem("Jake Gyllenhaal", "131")
	target.Overview = stringPtr("Stored overview.")
	target.PremiereDate = &birthday
	target.Values.ProductionLocations = []string{"Stored place"}
	target.ProviderIDs.Set("Imdb", "nm-stored")
	target.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/stored.jpg"}}

	if _, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &target); err != nil {
		t.Fatalf("RefreshPerson: %v", err)
	}
	if *target.Overview != "Stored overview." || !target.PremiereDate.Equal(birthday) {
		t.Errorf("Overview = %q, PremiereDate = %v; want the stored values", *target.Overview, target.PremiereDate)
	}
	if want := []string{"Stored place", "Los Angeles, California, USA"}; !reflect.DeepEqual(target.Values.ProductionLocations, want) {
		t.Errorf("ProductionLocations = %v, want %v", target.Values.ProductionLocations, want)
	}
	if got, _ := target.ProviderIDs.Get("Imdb"); got != "nm-stored" {
		t.Errorf("Imdb id = %q, want the stored id", got)
	}
	if len(target.ImageInfos) != 1 || target.ImageInfos[0].Path != "/stored.jpg" {
		t.Errorf("ImageInfos = %v, want the stored Primary image", target.ImageInfos)
	}

	replace := fullPersonRefresh()
	replace.ReplaceAllMetadata = true
	locked := personItem("Jake Gyllenhaal", "131")
	locked.Overview = stringPtr("Locked overview.")
	locked.LockedFields = []item.MetadataField{item.MetadataFieldOverview}
	locked.ProviderIDs.Set("Imdb", "nm-stored")
	if _, err := provider.RefreshPerson(t.Context(), replace, &locked); err != nil {
		t.Fatalf("RefreshPerson: %v", err)
	}
	if *locked.Overview != "Locked overview." {
		t.Errorf("Overview = %q, want the locked value", *locked.Overview)
	}
	if got, _ := locked.ProviderIDs.Get("Imdb"); got != "nm0350453" {
		t.Errorf("Imdb id = %q, want the replaced id", got)
	}

	whole := personItem("Jake Gyllenhaal", "131")
	whole.IsLocked = true
	if _, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &whole); err != nil {
		t.Fatalf("RefreshPerson: %v", err)
	}
	if whole.Overview != nil || len(whole.ImageInfos) != 1 {
		t.Errorf("locked person Overview = %v, images = %v; want images only", whole.Overview, whole.ImageInfos)
	}
}

// TestRefreshPersonUsesMetadataLanguage checks the language parameter
// (TmdbUtils.NormalizeLanguage) and that an untranslated biography has no
// English fallback, as upstream.
func TestRefreshPersonUsesMetadataLanguage(t *testing.T) {
	provider, _ := newPersonProvider(t)
	target := personItem("Jake Gyllenhaal", "131")
	request := fullPersonRefresh()
	request.Language, request.Country = "de-de", "DE"

	if handled, err := provider.RefreshPerson(t.Context(), request, &target); err != nil || !handled {
		t.Fatalf("RefreshPerson = %v, %v; want handled", handled, err)
	}
	if target.Overview != nil {
		t.Errorf("Overview = %q, want none for an empty biography", *target.Overview)
	}
	if target.PremiereDate == nil {
		t.Error("PremiereDate is nil, want the birthday")
	}
}

// TestRefreshPersonMapsDeathday maps deathday to EndDate.
func TestRefreshPersonMapsDeathday(t *testing.T) {
	provider, _ := newPersonProvider(t)
	target := personItem("Marlon Brando", "3084")
	if _, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &target); err != nil {
		t.Fatalf("RefreshPerson: %v", err)
	}
	if want := time.Date(2004, 7, 1, 0, 0, 0, 0, time.UTC); target.EndDate == nil || !target.EndDate.Equal(want) {
		t.Errorf("EndDate = %v, want %v", target.EndDate, want)
	}
}

// TestRefreshPersonInertWithoutKey keeps a provider without an API key inert.
func TestRefreshPersonInertWithoutKey(t *testing.T) {
	provider := tmdb.NewProvider(tmdb.Options{Config: tmdb.DefaultConfig()})
	target := personItem("Jake Gyllenhaal", "131")
	if handled, err := provider.RefreshPerson(t.Context(), fullPersonRefresh(), &target); err != nil || handled {
		t.Fatalf("RefreshPerson = %v, %v; want unhandled", handled, err)
	}
}
