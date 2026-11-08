// SPDX-License-Identifier: GPL-2.0-only

package storetest_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/livetv/catalog"
	"github.com/deadfin-org/deadfin/internal/livetv/guide"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/store/sqlite"
	"github.com/deadfin-org/deadfin/internal/store/storetest"
)

// TestLiveTvGuideSQLite stores a Live TV folder, channels and programmes as
// GuideManager does and reads the guide back through ItemQuery.
func TestLiveTvGuideSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runLiveTvGuide(t, stores)
}

// TestLiveTvGuidePostgres runs the same contract on PostgreSQL when
// DEADFIN_TEST_POSTGRES_DSN is set.
func TestLiveTvGuidePostgres(t *testing.T) {
	runLiveTvGuide(t, storetest.OpenPostgres(t))
}

func liveTvProgram(channel string, start time.Time, minutes int, title string) guide.Program {
	id := channel + "_" + start.Format("2006-01-02T15:04:05.0000000-07:00")
	p := guide.Program{
		ID:        id,
		ChannelID: channel,
		Name:      &title,
		StartDate: start,
		EndDate:   start.Add(time.Duration(minutes) * time.Minute),
		Genres:    []string{"sports"},
		IsSports:  true,
		ImageURL:  stringPtr("https://guide.example/" + title + ".png"),
		HasImage:  true,
	}
	etag, err := guide.Etag(p)
	if err != nil {
		panic(err)
	}
	p.Etag = etag
	return p
}

func runLiveTvGuide(t *testing.T, stores store.Stores) {
	t.Helper()
	ctx := t.Context()
	items := store.NewItemStore(stores)
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)

	folder := catalog.Folder("/data/metadata", "/data", now)
	if err := items.Upsert(ctx, folder); err != nil {
		t.Fatalf("upsert folder: %v", err)
	}
	infos := []catalog.ChannelInfo{
		{ID: "m3u_a", Name: "A", Number: "2.1", ImageURL: "https://logo.example/a.png"},
		{ID: "m3u_b", Name: "B", Number: "4.1"},
		{ID: "m3u_c", Name: "C", Number: "9.1", ChannelType: item.ChannelTypeRadio},
	}
	channels := make([]item.Item, 0, len(infos))
	for _, info := range infos {
		channels = append(channels, catalog.Channel(info, folder.ID, nil, now))
	}
	if result, err := items.ReconcileChildren(ctx, folder.ID, channels); err != nil || len(result.Inserted) != 3 {
		t.Fatalf("reconcile channels: %+v %v", result, err)
	}

	base := now.Add(-time.Hour)
	planA := []guide.Program{
		liveTvProgram("m3u_a", base, 60, "a1"),
		liveTvProgram("m3u_a", base.Add(time.Hour), 30, "a2"),
		liveTvProgram("m3u_a", base.Add(90*time.Minute), 30, "a3"),
		liveTvProgram("m3u_a", base.Add(2*time.Hour), 60, "a4"),
	}
	planB := []guide.Program{
		liveTvProgram("m3u_b", base.Add(30*time.Minute), 60, "b1"),
		liveTvProgram("m3u_b", base.Add(90*time.Minute), 60, "b2"),
	}
	storePrograms := func(channel item.Item, plan []guide.Program) store.ReconcileChildrenResult {
		t.Helper()
		existing, err := items.ReconciliationChildren(ctx, channel.ID)
		if err != nil {
			t.Fatalf("read programmes: %v", err)
		}
		byID := make(map[dto.UUID]item.Item, len(existing))
		for _, value := range existing {
			byID[value.ID] = value
		}
		desired := make([]item.Item, 0, len(plan))
		for _, program := range plan {
			var stored *item.Item
			if value, ok := byID[catalog.ProgramID(program.ID)]; ok {
				stored = &value
			}
			desired = append(desired, catalog.Program(program, channel, stored, now))
		}
		result, err := items.ReconcileChildren(ctx, channel.ID, desired)
		if err != nil {
			t.Fatalf("reconcile programmes: %v", err)
		}
		return result
	}
	if result := storePrograms(channels[0], planA); len(result.Inserted) != 4 {
		t.Fatalf("channel A: %+v", result)
	}
	if result := storePrograms(channels[1], planB); len(result.Inserted) != 2 {
		t.Fatalf("channel B: %+v", result)
	}

	// A second refresh with the same guide writes nothing.
	if result := storePrograms(channels[0], planA); len(result.Inserted)+len(result.Updated)+len(result.Deleted) != 0 {
		t.Fatalf("unchanged refresh wrote %+v", result)
	}
	stored, err := items.ReconciliationChildren(ctx, folder.ID)
	if err != nil {
		t.Fatal(err)
	}
	again := make([]item.Item, 0, len(stored))
	for index := range stored {
		again = append(again, catalog.Channel(infos[indexOfChannel(infos, stored[index])], folder.ID, &stored[index], now.Add(time.Hour)))
	}
	if result, err := items.ReconcileChildren(ctx, folder.ID, again); err != nil || len(result.Inserted)+len(result.Updated)+len(result.Deleted) != 0 {
		t.Fatalf("unchanged channel refresh wrote %+v %v", result, err)
	}

	program, err := items.Get(ctx, catalog.ProgramID(planA[1].ID))
	if err != nil {
		t.Fatalf("get programme: %v", err)
	}
	if program.TopParentID == nil || *program.TopParentID != folder.ID {
		t.Errorf("top parent %v, want folder %s", program.TopParentID, folder.ID)
	}
	if program.ChannelID == nil || *program.ChannelID != channels[0].ID {
		t.Errorf("channel %v", program.ChannelID)
	}
	if !reflect.DeepEqual(program.Values.Tags, []string{"Sports"}) || !reflect.DeepEqual(program.Values.Genres, []string{"sports"}) {
		t.Errorf("tags %q genres %q", program.Values.Tags, program.Values.Genres)
	}
	if catalog.StoredEtag(program) != planA[1].Etag {
		t.Errorf("etag %q", catalog.StoredEtag(program))
	}
	if len(program.ImageInfos) != 1 || program.ImageInfos[0].Path != "https://guide.example/a2.png" {
		t.Errorf("images %+v", program.ImageInfos)
	}

	// The guide grid: programmes under the folder that overlap a window, in
	// start order. Both bounds are inclusive (EndDate >= MinEndDate,
	// StartDate <= MaxStartDate), so a1, which ends at the window start, and
	// a4, which starts at its end, are in.
	windowStart, windowEnd := base.Add(time.Hour), base.Add(2*time.Hour)
	grid := store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindLiveTvProgram},
		TopParentIDs:     []dto.UUID{folder.ID},
		MinEndDate:       &windowStart,
		MaxStartDate:     &windowEnd,
		SortBy:           []store.ItemSortBy{store.ItemSortByStartDate, store.ItemSortBySortName},
	}
	assertProgrammeNames(t, items, grid, "a1", "b1", "a2", "a3", "b2", "a4")
	byChannel := store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindLiveTvProgram},
		ChannelIDs:       []dto.UUID{channels[1].ID},
		SortBy:           []store.ItemSortBy{store.ItemSortByStartDate},
	}
	assertProgrammeNames(t, items, byChannel, "b1", "b2")
	// The grid of a channel list, as jellyfin-web asks for it.
	channelGrid := grid
	channelGrid.ChannelIDs = []dto.UUID{channels[1].ID}
	assertProgrammeNames(t, items, channelGrid, "b1", "b2")
	translated, err := items.TranslateQuery(ctx, channelGrid)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	assertLiveTvPlan(t, stores, translated.SQL, translated.Args, "ix_items_top_kind_channel_start")
	for _, query := range []store.ItemQuery{grid, byChannel} {
		translated, err := items.TranslateQuery(ctx, query)
		if err != nil {
			t.Fatalf("translate: %v", err)
		}
		assertLiveTvPlan(t, stores, translated.SQL, translated.Args, "ix_items_top_kind_start", "ix_items_channel_start", "ix_items_top_kind_channel_start")
	}

	// A changed title updates one programme; a dropped one is deleted.
	changed := append([]guide.Program(nil), planA[:3]...)
	changed[2] = liveTvProgram("m3u_a", base.Add(90*time.Minute), 30, "a3 new")
	result := storePrograms(channels[0], changed)
	if len(result.Inserted) != 0 || len(result.Updated) != 1 || len(result.Deleted) != 1 {
		t.Fatalf("changed refresh: %+v", result)
	}
	assertProgrammeNames(t, items, store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindLiveTvProgram},
		ChannelIDs:       []dto.UUID{channels[0].ID},
		SortBy:           []store.ItemSortBy{store.ItemSortByStartDate},
	}, "a1", "a2", "a3 new")
}

func indexOfChannel(infos []catalog.ChannelInfo, channel item.Item) int {
	for index, info := range infos {
		if catalog.ChannelID(info.ID) == channel.ID {
			return index
		}
	}
	panic("unknown channel")
}

func assertProgrammeNames(t *testing.T, items *store.ItemStore, query store.ItemQuery, want ...string) {
	t.Helper()
	result, err := items.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := make([]string, 0, len(result.Items))
	for _, value := range result.Items {
		got = append(got, *value.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("programmes %q, want %q", got, want)
	}
}

// assertLiveTvPlan requires an index on the programme lookup: SQLite names
// one of the given indexes; PostgreSQL scans no table in full.
func assertLiveTvPlan(t *testing.T, stores store.Stores, statement string, args []any, indexes ...string) {
	t.Helper()
	if stores.Dialect().Placeholder(1) != "?" {
		assertScopedPlanAvoidsFullScans(t, stores, statement, args, []string{"items"})
		return
	}
	rows, err := stores.Reader().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var detail []string
	for rows.Next() {
		var id, parent, unused int
		var step string
		if err := rows.Scan(&id, &parent, &unused, &step); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		detail = append(detail, step)
	}
	plan := strings.Join(detail, "\n")
	for _, index := range indexes {
		if strings.Contains(plan, index+" ") {
			return
		}
	}
	t.Errorf("plan uses none of %v:\n%s", indexes, plan)
}

// TestLiveTvAiringSQLite checks the airing filter at the class limits of
// ix_items_airing (#892).
func TestLiveTvAiringSQLite(t *testing.T) {
	stores, err := sqlite.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	runLiveTvAiring(t, stores)
}

// TestLiveTvAiringPostgres runs the same contract on PostgreSQL when
// DEADFIN_TEST_POSTGRES_DSN is set.
func TestLiveTvAiringPostgres(t *testing.T) {
	runLiveTvAiring(t, storetest.OpenPostgres(t))
}

func runLiveTvAiring(t *testing.T, stores store.Stores) {
	t.Helper()
	ctx := t.Context()
	items := store.NewItemStore(stores)
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	folder := catalog.Folder("/data/metadata", "/data", now)
	if err := items.Upsert(ctx, folder); err != nil {
		t.Fatalf("upsert folder: %v", err)
	}
	channel := catalog.Channel(catalog.ChannelInfo{ID: "m3u_a", Name: "A", Number: "1"}, folder.ID, nil, now)
	if err := items.Upsert(ctx, channel); err != nil {
		t.Fatalf("upsert channel: %v", err)
	}
	// Each class limit gets a programme that ends at now with a span of
	// exactly the limit (airing), one a minute longer that started a
	// minute earlier and also ends at now (airing, next class), and one
	// with the limit's span that ended a minute ago.
	var programmes []guide.Program
	var want []string
	add := func(name string, start time.Time, span time.Duration, airing bool) {
		// The guide id holds the channel and the start; a distinct guide
		// channel keeps the ids of equal starts apart.
		programmes = append(programmes, liveTvProgram(fmt.Sprintf("m3u_a%d", len(programmes)), start, int(span/time.Minute), name))
		if airing {
			want = append(want, name)
		}
	}
	for _, limit := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour} {
		add("limit "+limit.String(), now.Add(-limit), limit, true)
		add("over "+limit.String(), now.Add(-limit-time.Minute), limit+time.Minute, true)
		add("ended "+limit.String(), now.Add(-limit-time.Minute), limit, false)
	}
	add("ten days", now.Add(-9*24*time.Hour), 10*24*time.Hour, true)
	add("starts now", now, 30*time.Minute, true)
	add("upcoming", now.Add(time.Minute), 30*time.Minute, false)
	desired := make([]item.Item, 0, len(programmes))
	for _, program := range programmes {
		desired = append(desired, catalog.Program(program, channel, nil, now))
	}
	if _, err := items.ReconcileChildren(ctx, channel.ID, desired); err != nil {
		t.Fatalf("reconcile programmes: %v", err)
	}

	airing := true
	query := store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindLiveTvProgram},
		TopParentIDs:     []dto.UUID{folder.ID},
		IsAiring:         &airing,
		ReferenceTime:    &now,
		SortBy:           []store.ItemSortBy{store.ItemSortByStartDate, store.ItemSortBySortName},
	}
	result, err := items.Query(ctx, query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := make(map[string]bool, len(result.Items))
	for _, value := range result.Items {
		got[*value.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("airing programmes miss %q", name)
		}
	}
	if len(result.Items) != len(want) {
		t.Errorf("airing programmes %d, want %d", len(result.Items), len(want))
	}
	translated, err := items.TranslateQuery(ctx, query)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	assertLiveTvPlan(t, stores, translated.SQL, translated.Args, "ix_items_airing")
}
