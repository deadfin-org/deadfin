// SPDX-License-Identifier: GPL-2.0-only

package people_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/people"
	"github.com/deadfin-org/deadfin/internal/providers"
)

var now = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

type fakeItems struct {
	mu      sync.Mutex
	byID    map[dto.UUID]item.Item
	missing []dto.UUID
	edited  []item.Item
}

func newFakeItems(values ...item.Item) *fakeItems {
	items := &fakeItems{byID: make(map[dto.UUID]item.Item)}
	for _, value := range values {
		items.byID[value.ID] = value
	}
	return items
}

func (items *fakeItems) GetBatch(_ context.Context, ids []dto.UUID) ([]item.Item, error) {
	items.mu.Lock()
	defer items.mu.Unlock()
	var out []item.Item
	for _, id := range ids {
		if value, found := items.byID[id]; found {
			out = append(out, value)
		}
	}
	return out, nil
}

func (items *fakeItems) EditItems(_ context.Context, edited []item.Item, _ []item.Item) error {
	items.mu.Lock()
	defer items.mu.Unlock()
	for _, value := range edited {
		items.byID[value.ID] = value
		items.edited = append(items.edited, value)
	}
	return nil
}

func (items *fakeItems) PeopleMissingMetadata(context.Context) ([]dto.UUID, error) {
	return items.missing, nil
}

type fakeProvider struct {
	mu       sync.Mutex
	requests []providers.PersonRefresh
	failFor  map[dto.UUID]bool
}

func (provider *fakeProvider) RefreshPerson(_ context.Context, request providers.PersonRefresh, target *item.Item) (bool, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	if provider.failFor[target.ID] {
		return false, errors.New("tmdb down")
	}
	if request.Metadata {
		overview := "A biography."
		target.Overview = &overview
	}
	if request.Images {
		target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: item.ImageTypePrimary, Path: "https://image.tmdb.org/t/p/original/a.jpg"})
	}
	return true, nil
}

type fakeSettings struct{}

func (fakeSettings) System() config.ServerConfiguration {
	return config.ServerConfiguration{PreferredMetadataLanguage: "de", MetadataCountryCode: "DE"}
}

func newService(t *testing.T, items *fakeItems, provider *fakeProvider) *people.Service {
	t.Helper()
	service, err := people.NewService(people.Options{Items: items, Provider: provider, Settings: fakeSettings{}, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func person(id byte) item.Item {
	name := "Person"
	return item.Item{ID: dto.UUID{id}, Kind: item.KindPerson, Name: &name}
}

func withMetadata(value item.Item) item.Item {
	overview := "Stored."
	value.Overview = &overview
	value.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/p.jpg"}}
	return value
}

func refreshedAt(value item.Item, at time.Time) item.Item {
	value.DateLastRefreshed = &at
	return value
}

// TestRefreshOnDemandFollowsRefreshItemOnDemandIfNeeded ports the GetItem
// condition: a Person without an overview or a Primary image, last refreshed
// 3 or more days ago or never, gets a full metadata and image refresh.
func TestRefreshOnDemandFollowsRefreshItemOnDemandIfNeeded(t *testing.T) {
	movie := item.Item{ID: dto.UUID{9}, Kind: item.KindMovie}
	imageOnly := person(6)
	imageOnly.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/p.jpg"}}
	cases := []struct {
		name  string
		value item.Item
		want  bool
	}{
		{"never refreshed", person(1), true},
		{"refreshed 3 days ago", refreshedAt(person(2), now.Add(-72*time.Hour)), true},
		{"refreshed 2 days ago", refreshedAt(person(3), now.Add(-48*time.Hour)), false},
		{"has metadata", withMetadata(person(4)), false},
		{"not a person", movie, false},
		{"image but no overview", imageOnly, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			items, provider := newFakeItems(test.value), &fakeProvider{}
			got, refreshed, err := newService(t, items, provider).RefreshOnDemand(t.Context(), test.value)
			if err != nil {
				t.Fatalf("RefreshOnDemand: %v", err)
			}
			if refreshed != test.want {
				t.Fatalf("refreshed = %v, want %v", refreshed, test.want)
			}
			if !test.want {
				if len(provider.requests) != 0 || len(items.edited) != 0 {
					t.Fatalf("requests = %v, edits = %d; want none", provider.requests, len(items.edited))
				}
				return
			}
			want := providers.PersonRefresh{Language: "de", Country: "DE", Metadata: true, Images: true}
			if !reflect.DeepEqual(provider.requests, []providers.PersonRefresh{want}) {
				t.Errorf("requests = %+v, want %+v", provider.requests, want)
			}
			if got.Overview == nil || got.DateLastRefreshed == nil || !got.DateLastRefreshed.Equal(now) {
				t.Errorf("refreshed item Overview = %v, DateLastRefreshed = %v; want the biography and now", got.Overview, got.DateLastRefreshed)
			}
			if len(items.edited) != 1 {
				t.Errorf("edits = %d, want 1", len(items.edited))
			}
		})
	}
}

// TestRefreshOnDemandProviderFailureKeepsRefreshDate checks that a failed
// provider writes nothing, so the next view tries again.
func TestRefreshOnDemandProviderFailureKeepsRefreshDate(t *testing.T) {
	value := person(1)
	items, provider := newFakeItems(value), &fakeProvider{failFor: map[dto.UUID]bool{value.ID: true}}
	_, refreshed, err := newService(t, items, provider).RefreshOnDemand(t.Context(), value)
	if err != nil || refreshed {
		t.Fatalf("RefreshOnDemand = %v, %v; want not refreshed, no error", refreshed, err)
	}
	if len(items.edited) != 0 {
		t.Fatalf("edits = %d, want none", len(items.edited))
	}
}

// TestRunRefreshesNeverRefreshedPeople ports RefreshPersonAsync: the request
// asks only for the part the person lacks, a refreshed person is skipped,
// and one failure does not stop the task.
func TestRunRefreshesNeverRefreshedPeople(t *testing.T) {
	bare := person(1)
	imageOnly := person(2)
	imageOnly.ImageInfos = []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/p.jpg"}}
	failing := person(3)
	old := refreshedAt(person(4), now.Add(-90*24*time.Hour))
	items := newFakeItems(bare, imageOnly, failing, old)
	items.missing = []dto.UUID{bare.ID, imageOnly.ID, failing.ID, old.ID, {0x55}}
	provider := &fakeProvider{failFor: map[dto.UUID]bool{failing.ID: true}}
	var progress []float64
	if err := newService(t, items, provider).Run(t.Context(), func(value float64) { progress = append(progress, value) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []providers.PersonRefresh{
		{Language: "de", Country: "DE", Metadata: true, Images: true},
		{Language: "de", Country: "DE", Metadata: true},
		{Language: "de", Country: "DE", Metadata: true, Images: true},
	}
	if !reflect.DeepEqual(provider.requests, want) {
		t.Errorf("requests = %+v, want %+v", provider.requests, want)
	}
	if len(items.edited) != 2 || items.edited[0].ID != bare.ID || items.edited[1].ID != imageOnly.ID {
		t.Errorf("edited = %d items, want bare and imageOnly", len(items.edited))
	}
	if len(progress) == 0 || progress[0] != 0 || progress[len(progress)-1] != 100 {
		t.Errorf("progress = %v, want 0 to 100", progress)
	}
}

// TestRunStopsOnCancel returns the context error.
func TestRunStopsOnCancel(t *testing.T) {
	items := newFakeItems(person(1))
	items.missing = []dto.UUID{{1}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := newService(t, items, &fakeProvider{}).Run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}
