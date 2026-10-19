// SPDX-License-Identifier: GPL-2.0-only

package library_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/naming"
)

func TestResolverPriorityLedger(t *testing.T) {
	for _, tt := range []struct {
		name string
		got  library.ResolverPriority
		want library.ResolverPriority
	}{
		{"plugin", library.ResolverPriorityPlugin, 0},
		{"first", library.ResolverPriorityFirst, 1},
		{"second", library.ResolverPrioritySecond, 2},
		{"third", library.ResolverPriorityThird, 3},
		{"fourth", library.ResolverPriorityFourth, 4},
		{"fifth", library.ResolverPriorityFifth, 5},
		{"last", library.ResolverPriorityLast, 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("resolver priority %s = %d, want pinned ordinal %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestResolverChainUsesPriorityAndStableFirstMatch(t *testing.T) {
	var calls []string
	chain := library.NewChain(
		resolverProbe{priority: library.ResolverPriorityFourth, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
			calls = append(calls, "later")
			return itemWithName(item.KindFolder, "later"), nil
		}},
		resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
			calls = append(calls, "first")
			return itemWithName(item.KindMovie, "registered-first"), nil
		}},
		resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
			calls = append(calls, "same-priority-later")
			return itemWithName(item.KindAudio, "registered-second"), nil
		}},
		resolverProbe{priority: library.ResolverPriorityPlugin, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
			calls = append(calls, "plugin-no-match")
			return nil, nil
		}},
	)

	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/media/input.mkv"}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Name == nil || *got.Name != "registered-first" || got.Kind != item.KindMovie {
		t.Fatalf("Resolve() = %#v, want first registered First-priority movie", got)
	}
	if want := []string{"plugin-no-match", "first"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("resolver calls = %#v, want source priority order %#v with first match stopping chain", calls, want)
	}
}

func TestResolverChainNoMatchErrorsAndCancellation(t *testing.T) {
	boom := errors.New("resolver failed")
	for _, tt := range []struct {
		name  string
		ctx   func() context.Context
		chain *library.Chain
		want  error
	}{
		{
			name:  "no match returns nil item and nil error",
			ctx:   context.Background,
			chain: library.NewChain(resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) { return nil, nil }}),
		},
		{
			name: "resolver error is surfaced without trying a lower priority claim",
			ctx:  context.Background,
			chain: library.NewChain(
				resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) { return nil, boom }},
				resolverProbe{priority: library.ResolverPriorityLast, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
					t.Fatal("lower-priority resolver ran after error")
					return nil, nil
				}},
			),
			want: boom,
		},
		{
			name: "pre-canceled context is returned before a resolver can run",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			chain: library.NewChain(resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
				t.Fatal("resolver ran with canceled context")
				return nil, nil
			}}),
			want: context.Canceled,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.chain.Resolve(tt.ctx(), library.ResolveContext{Entry: library.Entry{Path: "/media/input"}})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, tt.want)
			}
			if got != nil {
				t.Fatalf("Resolve() item = %#v, want nil", got)
			}
		})
	}
}

func TestResolverChainMultiResolverUsesPriorityAndFirstNonEmptyGroup(t *testing.T) {
	var calls []string
	movie := itemWithName(item.KindMovie, "movie group")
	chain := library.NewChain(
		multiResolverProbe{resolverProbe: resolverProbe{priority: library.ResolverPriorityFifth}, resolveMany: func(context.Context, library.ResolveContext) ([]*item.Item, error) {
			calls = append(calls, "audio")
			return []*item.Item{itemWithName(item.KindAudio, "audio group")}, nil
		}},
		multiResolverProbe{resolverProbe: resolverProbe{priority: library.ResolverPriorityFourth}, resolveMany: func(context.Context, library.ResolveContext) ([]*item.Item, error) {
			calls = append(calls, "movie")
			return []*item.Item{movie}, nil
		}},
	)

	got, err := chain.ResolveMany(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/media"}})
	if err != nil {
		t.Fatalf("ResolveMany() error = %v", err)
	}
	if !reflect.DeepEqual(got, []*item.Item{movie}) {
		t.Fatalf("ResolveMany() = %#v, want first non-empty Movie grouping %#v", got, []*item.Item{movie})
	}
	if want := []string{"movie"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("multi-resolver calls = %#v, want lower-priority multi resolver skipped after grouped result %#v", calls, want)
	}
}

func TestResolverChainMultiResolverFallsThroughEmptyAndSurfacesErrors(t *testing.T) {
	boom := errors.New("group failed")
	for _, tt := range []struct {
		name  string
		chain *library.Chain
		want  error
	}{
		{
			name: "empty higher-priority grouping falls through",
			chain: library.NewChain(
				multiResolverProbe{resolverProbe: resolverProbe{priority: library.ResolverPriorityFourth}, resolveMany: func(context.Context, library.ResolveContext) ([]*item.Item, error) { return nil, nil }},
				multiResolverProbe{resolverProbe: resolverProbe{priority: library.ResolverPriorityFifth}, resolveMany: func(context.Context, library.ResolveContext) ([]*item.Item, error) {
					return []*item.Item{itemWithName(item.KindAudio, "book chapter")}, nil
				}},
			),
		},
		{
			name:  "grouping error is returned",
			chain: library.NewChain(multiResolverProbe{resolverProbe: resolverProbe{priority: library.ResolverPriorityFourth}, resolveMany: func(context.Context, library.ResolveContext) ([]*item.Item, error) { return nil, boom }}),
			want:  boom,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.chain.ResolveMany(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/media"}})
			if !errors.Is(err, tt.want) {
				t.Fatalf("ResolveMany() error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (len(got) != 1 || got[0].Kind != item.KindAudio) {
				t.Fatalf("ResolveMany() = %#v, want lower-priority non-empty audio group", got)
			}
		})
	}
}

func TestDefaultResolverPriorityLedger(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	got := resolverPriorities(chain.Resolvers())
	want := []library.ResolverPriority{
		library.ResolverPriorityFirst, library.ResolverPriorityFirst, library.ResolverPriorityFirst,
		library.ResolverPriorityFirst, library.ResolverPriorityFirst, library.ResolverPriorityFirst,
		library.ResolverPriorityFirst, library.ResolverPriorityFirst,
		library.ResolverPrioritySecond, library.ResolverPrioritySecond, library.ResolverPrioritySecond,
		library.ResolverPriorityThird, library.ResolverPriorityFourth, library.ResolverPriorityFifth,
		library.ResolverPriorityLast,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default resolver priorities = %#v, want source ledger %#v", got, want)
	}
}

func TestDefaultResolversTranslateSourceLedgerCases(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0x91}, Kind: item.KindFolder}
	series := item.Item{ID: dto.UUID{0x92}, Kind: item.KindSeries}
	season := item.Item{ID: dto.UUID{0x93}, Kind: item.KindSeason}

	for _, tt := range []struct {
		name string
		in   library.ResolveContext
		kind item.Kind
	}{
		{"special physical root", library.ResolveContext{Entry: library.Entry{Path: "/missing/media-root", IsDir: true}, PhysicalRoot: true}, item.KindAggregateFolder},
		{"special virtual folder", library.ResolveContext{Entry: library.Entry{Path: "/missing/Movies", IsDir: true}, IsVirtualFolder: true, CollectionType: item.CollectionTypeMovies}, item.KindCollectionFolder},
		{"series", library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Synthetic Show (2024)", IsDir: true}, Parent: &parent, CollectionType: item.CollectionTypeTVShows}, item.KindSeries},
		{"music artist", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Goldenrec Generator", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/music/Goldenrec Generator/Albums", IsDir: true}}, CollectionType: item.CollectionTypeMusic}, item.KindMusicArtist},
		{"photo album", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Holiday", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/photos/Holiday/IMG_0001.jpg"}}, CollectionType: item.CollectionTypePhotos}, item.KindPhotoAlbum},
		{"music album", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Goldenrec Generator/Synthetic Album", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3"}}, CollectionType: item.CollectionTypeMusic}, item.KindMusicAlbum},
		{"movie odd unicode filename", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/Odd Names/Café déjà-vu (2020).mkv", Size: 42}, Parent: &parent, CollectionType: item.CollectionTypeMovies}, item.KindMovie},
		{"audio fixture filename", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Goldenrec Generator/Synthetic Album/1 - Synthetic Track 1.mp3", Size: 42}, Parent: &parent, CollectionType: item.CollectionTypeMusic}, item.KindAudio},
		{"folder fallback", library.ResolveContext{Entry: library.Entry{Path: "/missing/misc/Unclaimed", IsDir: true}, Parent: &parent}, item.KindFolder},
		{"physical library root falls through to folder", library.ResolveContext{Entry: library.Entry{Path: "/missing/Movies/Source", IsDir: true}, Parent: &parent, IsPhysicalLibraryRoot: true, CollectionType: item.CollectionTypeMovies}, item.KindFolder},
		{"season", library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Synthetic Show (2024)/Season 01", IsDir: true}, Parent: &series, CollectionType: item.CollectionTypeTVShows}, item.KindSeason},
		{"episode special season", library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Synthetic Show (2024)/Specials/Synthetic Show S00E02 - Behind The Bars.mkv", Size: 42}, Parent: &season, CollectionType: item.CollectionTypeTVShows}, item.KindEpisode},
		{"photo", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Holiday/IMG_0001.jpg", Size: 42}, Parent: &parent, CollectionType: item.CollectionTypePhotos}, item.KindPhoto},
		{"playlist", library.ResolveContext{Entry: library.Entry{Path: "/missing/playlist/Road Trip [playlist]", IsDir: true}, Parent: &parent}, item.KindPlaylist},
		{"book", library.ResolveContext{Entry: library.Entry{Path: "/missing/books/A Study in Scarlet (Sherlock Holmes, #1) (1887).epub", Size: 42}, Parent: &parent, CollectionType: item.CollectionTypeBooks}, item.KindBook},
		{"box set", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/Synthetic Collection [boxset]", IsDir: true}, Parent: &parent, CollectionType: item.CollectionTypeMovies}, item.KindBoxSet},
		{"theme video extra", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/300/backdrops/300.mp4", Size: 42}, Parent: &parent, LibraryRoot: "/missing/movies", CollectionType: item.CollectionTypeMovies}, item.KindVideo},
		{"theme song extra", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/300/theme-music/300.mp3", Size: 42}, Parent: &parent, LibraryRoot: "/missing/movies", CollectionType: item.CollectionTypeMovies}, item.KindAudio},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), tt.in)
			if err != nil {
				t.Fatalf("Resolve(%#v) error = %v", tt.in, err)
			}
			if got == nil || got.Kind != tt.kind {
				t.Fatalf("Resolve(%#v) = %#v, want kind %s", tt.in, got, tt.kind)
			}
		})
	}
}

func TestDefaultResolversClassifyThemeExtras(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xb1}, Kind: item.KindMovie}
	for _, tt := range []struct {
		name, path string
		kind       item.Kind
		extra      item.ExtraType
	}{
		{"theme video in backdrops", "/missing/movies/300/backdrops/300.mp4", item.KindVideo, item.ExtraTypeThemeVideo},
		{"theme song in theme-music", "/missing/movies/300/theme-music/300.mp3", item.KindAudio, item.ExtraTypeThemeSong},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), library.ResolveContext{
				Entry:          library.Entry{Path: tt.path, Size: 42},
				Parent:         &parent,
				LibraryRoot:    "/missing/movies",
				CollectionType: item.CollectionTypeMovies,
			})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.path, err)
			}
			if got == nil || got.Kind != tt.kind || got.ExtraType == nil || *got.ExtraType != tt.extra {
				t.Fatalf("Resolve(%q) = %#v, want %s %s", tt.path, got, tt.kind, tt.extra)
			}
		})
	}
}

func TestDefaultMultiResolversHonorMovieThenAudioSourcePriority(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xc1}, Kind: item.KindFolder}
	for _, tt := range []struct {
		name  string
		input library.ResolveContext
		kind  item.Kind
		count int
	}{
		{
			name: "movie grouping wins before lower-priority audio",
			input: library.ResolveContext{
				Parent:         &parent,
				CollectionType: item.CollectionTypeMovies,
				Children: []library.Entry{
					{Path: "/missing/movies/First Film (2020).mkv", Size: 11},
					{Path: "/missing/movies/Second Film (2021).mkv", Size: 12},
					{Path: "/missing/movies/theme-music/theme.mp3", Size: 13},
					{Path: "/missing/movies/trailer.mp4", Size: 14},
				},
			},
			kind: item.KindMovie, count: 2,
		},
		{
			name: "single book audio grouping produces an audiobook",
			input: library.ResolveContext{
				Parent:         &parent,
				CollectionType: item.CollectionTypeBooks,
				Children:       []library.Entry{{Path: "/missing/books/Larry Potter/Book.mp3", Size: 11}},
			},
			kind: item.KindAudioBook, count: 1,
		},
		{
			name: "multi-file audiobook stack remains unclaimed for later extras handling",
			input: library.ResolveContext{
				Parent:         &parent,
				CollectionType: item.CollectionTypeBooks,
				Children: []library.Entry{
					{Path: "/missing/books/Larry Potter/Chapter 1.mp3", Size: 11},
					{Path: "/missing/books/Larry Potter/Chapter 2.mp3", Size: 12},
				},
			},
			count: 0,
		},
		{
			name: "audio does not multi-resolve ordinary music children",
			input: library.ResolveContext{
				Parent:         &parent,
				CollectionType: item.CollectionTypeMusic,
				Children:       []library.Entry{{Path: "/missing/music/Album/Track.mp3", Size: 11}},
			},
			count: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.ResolveMany(t.Context(), tt.input)
			if err != nil {
				t.Fatalf("ResolveMany() error = %v", err)
			}
			if len(got) != tt.count {
				t.Fatalf("ResolveMany() returned %d items %#v, want %d", len(got), got, tt.count)
			}
			for _, resolved := range got {
				if resolved == nil || resolved.Kind != tt.kind {
					t.Fatalf("ResolveMany() item = %#v, want %s", resolved, tt.kind)
				}
			}
		})
	}
}

// TestStackedVideosPreserveSecondaryPaths pins Video.AdditionalParts' source
// ordering: the first resolved file remains the primary item and every later
// stack file is retained for Videos/{itemId}/AdditionalParts lookup.
func TestStackedVideosPreserveSecondaryPaths(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	primary := "/missing/movies/Stacked Film (2024) part1.mkv"
	parts := []string{
		"/missing/movies/Stacked Film (2024) part2.mkv",
		"/missing/movies/Stacked Film (2024) part3.mkv",
	}
	got, err := chain.ResolveMany(t.Context(), library.ResolveContext{
		CollectionType: item.CollectionTypeMovies,
		Children: []library.Entry{
			{Path: primary, Size: 11},
			{Path: parts[0], Size: 12},
			{Path: parts[1], Size: 13},
		},
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("ResolveMany(stacked video) = %#v, %v; want one primary", got, err)
	}
	if got[0].Path == nil || *got[0].Path != primary || !reflect.DeepEqual(got[0].AdditionalParts, parts) {
		t.Fatalf("stacked video = %#v, want primary %q and parts %#v", got[0], primary, parts)
	}
}

func TestNewChainSkipsNilResolvers(t *testing.T) {
	chain := library.NewChain(nil, resolverProbe{priority: library.ResolverPriorityFirst, resolve: func(context.Context, library.ResolveContext) (*item.Item, error) {
		return itemWithName(item.KindFolder, "claimed"), nil
	}})
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/missing"}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Name == nil || *got.Name != "claimed" {
		t.Fatalf("Resolve() = %#v, want non-nil resolver claim", got)
	}
}

func TestDefaultChainSnapshotsNamingOptions(t *testing.T) {
	options := naming.DefaultOptions()
	chain := library.NewDefaultChain(options)
	for index := range options.VideoFileExtensions {
		options.VideoFileExtensions[index] = ".not-a-video-extension"
	}
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/Snapshot Film (2024).mkv"}, CollectionType: item.CollectionTypeMovies})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Kind != item.KindMovie {
		t.Fatalf("Resolve() after caller mutates naming options = %#v, want snapshot-backed Movie", got)
	}
}

func TestDefaultResolversUseSourceCLRTypeIdentity(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	for _, tt := range []struct {
		name, path string
		collection item.CollectionType
		want       dto.UUID
	}{
		{"book", "/var/lib/jellyfin/data/metadata/Book.epub", item.CollectionTypeBooks, dto.UUID{0x4a, 0x0d, 0x9a, 0x15, 0xce, 0x98, 0x44, 0x6e, 0x83, 0x64, 0x33, 0xe0, 0xa4, 0x02, 0x09, 0xd7}},
		{"playlist", "/var/lib/jellyfin/data/metadata/Road.m3u", item.CollectionTypeMusic, dto.UUID{0x22, 0xfb, 0x7f, 0x2e, 0x05, 0xb2, 0x55, 0xd8, 0xb7, 0x93, 0xf6, 0x8e, 0x3a, 0xd7, 0x57, 0x82}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: tt.path}, CollectionType: tt.collection, ProgramDataRoot: "/var/lib/jellyfin/data"})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.path, err)
			}
			if got == nil || got.ID != tt.want {
				t.Fatalf("Resolve(%q).ID = %v, want source CLR identity %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestBookAudioUsesAudioBookKindAndPreservesInitializerContext(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xe1}, Kind: item.KindFolder, IsLocked: true}
	entry := library.Entry{Path: "/var/lib/jellyfin/data/books/Larry Potter/Chapter 1.mp3", Size: 123}
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: entry, Parent: &parent, CollectionType: item.CollectionTypeBooks, ProgramDataRoot: "/var/lib/jellyfin/data", CaseSensitiveIDs: true})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Kind != item.KindAudioBook || !got.IsLocked || got.ParentID == nil || *got.ParentID != parent.ID {
		t.Fatalf("Resolve(book audio) = %#v, want initialized locked AudioBook", got)
	}
}

func TestMusicVideoCollectionUsesMusicVideoKind(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/missing/music-videos/Artist - Song.mkv"}, CollectionType: item.CollectionTypeMusicVideos})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Kind != item.KindMusicVideo {
		t.Fatalf("Resolve(music video) = %#v, want MusicVideo", got)
	}
}

func TestDefaultResolversRespectSourceAdmissionBoundaries(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xd1}, Kind: item.KindFolder}
	artistParent := item.Item{ID: dto.UUID{0xd2}, Kind: item.KindMusicArtist}
	albumParent := item.Item{ID: dto.UUID{0xd3}, Kind: item.KindMusicAlbum}
	seriesParent := item.Item{ID: dto.UUID{0xd4}, Kind: item.KindSeries}
	for _, tt := range []struct {
		name     string
		input    library.ResolveContext
		wantKind item.Kind
		noMatch  bool
		wantPath string
	}{
		{
			name:    "photo files outside a photo-enabled collection are not photos",
			input:   library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/feature-poster.jpg"}, Parent: &parent, CollectionType: item.CollectionTypeMovies},
			noMatch: true,
		},
		{
			name:     "empty photo folder falls through rather than becoming an album",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Empty", IsDir: true}, Parent: &parent, CollectionType: item.CollectionTypePhotos},
			wantKind: item.KindFolder,
		},
		{
			name:     "nested series folders do not become another series",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Show/Nested", IsDir: true}, Parent: &seriesParent, CollectionType: item.CollectionTypeTVShows},
			wantKind: item.KindFolder,
		},
		{
			name:     "nested artist folders do not become artists",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Artist/Nested", IsDir: true}, Parent: &artistParent, Children: []library.Entry{{Path: "/missing/music/Artist/Nested/Album", IsDir: true}}, CollectionType: item.CollectionTypeMusic},
			wantKind: item.KindFolder,
		},
		{
			name:     "nested album folders do not become albums",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Artist/Album/Disc 1", IsDir: true}, Parent: &albumParent, Children: []library.Entry{{Path: "/missing/music/Artist/Album/Disc 1/Track.mp3"}}, CollectionType: item.CollectionTypeMusic},
			wantKind: item.KindFolder,
		},
		{
			name:    "movie resolver rejects video in music collection",
			input:   library.ResolveContext{Entry: library.Entry{Path: "/missing/music/not-a-movie.mkv"}, Parent: &parent, CollectionType: item.CollectionTypeMusic},
			noMatch: true,
		},
		{
			name:    "music playlist files are not playlists in movie libraries",
			input:   library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/not-a-playlist.m3u"}, Parent: &parent, CollectionType: item.CollectionTypeMovies},
			noMatch: true,
		},
		{
			name:     "directory playlist is recognized from cached playlist.xml",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Shared", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/music/Shared/playlist.xml"}}, CollectionType: item.CollectionTypeMusic},
			wantKind: item.KindPlaylist,
		},
		{
			name:     "box set is recognized from cached collection.xml",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/Collected", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/movies/Collected/collection.xml"}}, CollectionType: item.CollectionTypeMovies},
			wantKind: item.KindBoxSet,
		},
		{
			name:     "book folder with exactly one supported entry becomes a book",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/books/Study", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/books/Study/A Study in Scarlet (Sherlock Holmes, #1) (1887).epub"}}, CollectionType: item.CollectionTypeBooks},
			wantKind: item.KindBook,
			wantPath: "/missing/books/Study/A Study in Scarlet (Sherlock Holmes, #1) (1887).epub",
		},
		{
			name:     "music playlist filename drops its extension",
			input:    library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Road Trip.m3u"}, Parent: &parent, CollectionType: item.CollectionTypeMusic},
			wantKind: item.KindPlaylist,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), tt.input)
			if err != nil {
				t.Fatalf("Resolve(%#v) error = %v", tt.input, err)
			}
			if tt.noMatch {
				if got != nil {
					t.Fatalf("Resolve(%#v) = %#v, want no resolver claim", tt.input, got)
				}
				return
			}
			if got == nil || got.Kind != tt.wantKind {
				t.Fatalf("Resolve(%#v) = %#v, want %s", tt.input, got, tt.wantKind)
			}
			if tt.wantPath != "" && (got.Path == nil || *got.Path != tt.wantPath) {
				t.Fatalf("Resolve(%#v).Path = %v, want supported child %q", tt.input, got.Path, tt.wantPath)
			}
			if tt.wantKind == item.KindPlaylist && got.Name != nil && *got.Name == "Road Trip.m3u" {
				t.Fatalf("playlist name = %q, want extension-free source name", *got.Name)
			}
		})
	}
}

func TestDefaultResolversPreserveTVHierarchyOverParsedNames(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	seriesName := "Canonical Show"
	seasonName := "Canonical Season"
	series := item.Item{ID: dto.UUID{0xf1}, Kind: item.KindSeries, Name: &seriesName}
	season := item.Item{ID: dto.UUID{0xf2}, Kind: item.KindSeason, Name: &seasonName}
	got, err := chain.Resolve(t.Context(), library.ResolveContext{
		Entry:          library.Entry{Path: "/missing/tv/Canonical Show/Season 1/Wrong Show S01E01.mkv"},
		Parent:         &season,
		Ancestors:      []*item.Item{&series},
		CollectionType: item.CollectionTypeTVShows,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Kind != item.KindEpisode || got.SeriesID == nil || *got.SeriesID != series.ID || got.SeasonID == nil || *got.SeasonID != season.ID || got.SeriesName == nil || *got.SeriesName != seriesName || got.SeasonName == nil || *got.SeasonName != seasonName {
		t.Fatalf("Resolve(episode under canonical hierarchy) = %#v, want hierarchy IDs and names over parsed filename", got)
	}

	nested, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Canonical Show/Season 1/Season 2", IsDir: true}, Parent: &season, Ancestors: []*item.Item{&series}, CollectionType: item.CollectionTypeTVShows})
	if err != nil {
		t.Fatalf("Resolve(nested season) error = %v", err)
	}
	if nested == nil || nested.Kind != item.KindFolder {
		t.Fatalf("Resolve(nested season) = %#v, want Folder because only a direct Series child is a Season", nested)
	}
}

func TestDefaultResolversApplyBookAndPlaylistSourceDetails(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xf3}, Kind: item.KindFolder}
	for _, tt := range []struct {
		name, path, wantName, wantSeries string
		collection                       item.CollectionType
		kind                             item.Kind
	}{
		{"cb7 book", "/missing/books/Comics/Series 1.cb7", "Series 1", "Comics", item.CollectionTypeBooks, item.KindBook},
		{"playlist marker name", "/missing/music/Road Trip [PLAYLIST]", "Road Trip", "", item.CollectionTypeMusic, item.KindPlaylist},
		{"box set marker name", "/missing/movies/Collected [BOXSET]", "Collected", "", item.CollectionTypeMovies, item.KindBoxSet},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: tt.path, IsDir: tt.kind != item.KindBook}, Parent: &parent, CollectionType: tt.collection})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.path, err)
			}
			if got == nil || got.Kind != tt.kind || got.Name == nil || *got.Name != tt.wantName {
				t.Fatalf("Resolve(%q) = %#v, want %s named %q", tt.path, got, tt.kind, tt.wantName)
			}
			if tt.wantSeries != "" && (got.SeriesName == nil || *got.SeriesName != tt.wantSeries) {
				t.Fatalf("Resolve(%q).SeriesName = %v, want containing folder %q", tt.path, got.SeriesName, tt.wantSeries)
			}
		})
	}
}

func TestDefaultResolversKeepMusicAudioOutOfAudiobookParsing(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Album/Part 3 - Chapter 2.mp3"}, CollectionType: item.CollectionTypeMusic})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got == nil || got.Kind != item.KindAudio || got.IndexNumber != nil || got.ParentIndexNumber != nil {
		t.Fatalf("Resolve(music chapter-shaped filename) = %#v, want ordinary Audio without audiobook chapter/part facts", got)
	}
}

func TestDefaultResolversRespectPhotoAudioArtistMovieAndTrailerAdmissions(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xf4}, Kind: item.KindFolder}
	for _, tt := range []struct {
		name    string
		input   library.ResolveContext
		kind    item.Kind
		noMatch bool
	}{
		{"poster image is metadata not a photo", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/poster.jpg"}, Parent: &parent, CollectionType: item.CollectionTypePhotos}, 0, true},
		{"album with only poster is not photo album", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Poster Only", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/photos/Poster Only/poster.jpg"}}, CollectionType: item.CollectionTypePhotos}, item.KindFolder, false},
		{"album photo owned by movie is not photo album", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Film", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/photos/Film/Film.mkv"}, {Path: "/missing/photos/Film/Film-poster.jpg"}}, CollectionType: item.CollectionTypePhotos}, item.KindFolder, false},
		{"ordinary audio is rejected in movie collection", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/Track.mp3"}, Parent: &parent, CollectionType: item.CollectionTypeMovies}, 0, true},
		{"empty child directory is not sufficient to identify an artist", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Maybe Artist", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/music/Maybe Artist/Empty", IsDir: true}}, CollectionType: item.CollectionTypeMusic}, item.KindFolder, false},
		{"home video collection admits generic video", library.ResolveContext{Entry: library.Entry{Path: "/missing/home/Clip.mkv"}, Parent: &parent, CollectionType: item.CollectionTypeHomeVideos}, item.KindVideo, false},
		{"unknown collection admits generic video", library.ResolveContext{Entry: library.Entry{Path: "/missing/mixed/Clip.mkv"}, Parent: &parent}, item.KindVideo, false},
		{"trailer extra uses trailer item kind", library.ResolveContext{Entry: library.Entry{Path: "/missing/movies/trailer.mp4"}, Parent: &parent, LibraryRoot: "/missing/movies", CollectionType: item.CollectionTypeMovies}, item.KindTrailer, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), tt.input)
			if err != nil {
				t.Fatalf("Resolve(%#v) error = %v", tt.input, err)
			}
			if tt.noMatch {
				if got != nil {
					t.Fatalf("Resolve(%#v) = %#v, want no claim", tt.input, got)
				}
				return
			}
			if got == nil || got.Kind != tt.kind {
				t.Fatalf("Resolve(%#v) = %#v, want %s", tt.input, got, tt.kind)
			}
		})
	}
}

func TestDefaultResolversHandleResidualSourceCases(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xf5}, Kind: item.KindFolder}
	seriesName := "Show"
	series := item.Item{ID: dto.UUID{0xf6}, Kind: item.KindSeries, Name: &seriesName}
	season := item.Item{ID: dto.UUID{0xf7}, Kind: item.KindSeason}
	album := item.Item{ID: dto.UUID{0xf8}, Kind: item.KindMusicAlbum}
	for _, tt := range []struct {
		name    string
		input   library.ResolveContext
		kind    item.Kind
		noMatch bool
		unsetTV bool
	}{
		{"unparsed video below season remains episode", library.ResolveContext{Entry: library.Entry{Path: "/missing/tv/Show/Season 1/Scene.mkv"}, Parent: &season, Ancestors: []*item.Item{&series}, CollectionType: item.CollectionTypeTVShows}, item.KindEpisode, false, true},
		{"cue file remains unclaimed", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Album/Album.cue"}, Parent: &parent, CollectionType: item.CollectionTypeMusic}, 0, true, false},
		{"album is rejected below any album ancestor", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Artist/Album/Disc 1", IsDir: true}, Parent: &parent, Ancestors: []*item.Item{&album}, Children: []library.Entry{{Path: "/missing/music/Artist/Album/Disc 1/Track.mp3"}}, CollectionType: item.CollectionTypeMusic}, item.KindFolder, false, false},
		{"m4v owned artwork is not photo album content", library.ResolveContext{Entry: library.Entry{Path: "/missing/photos/Film", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/photos/Film/Film.m4v"}, {Path: "/missing/photos/Film/Film-poster.jpg"}}, CollectionType: item.CollectionTypePhotos}, item.KindFolder, false, false},
		{"mixed library tvshow marker resolves series", library.ResolveContext{Entry: library.Entry{Path: "/missing/mixed/Show", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/mixed/Show/tvshow.nfo"}}}, item.KindSeries, false, false},
		{"album named empty child is not artist evidence", library.ResolveContext{Entry: library.Entry{Path: "/missing/music/Maybe Artist", IsDir: true}, Parent: &parent, Children: []library.Entry{{Path: "/missing/music/Maybe Artist/An Album", IsDir: true}}, CollectionType: item.CollectionTypeMusic}, item.KindFolder, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), tt.input)
			if err != nil {
				t.Fatalf("Resolve(%#v) error = %v", tt.input, err)
			}
			if tt.noMatch {
				if got != nil {
					t.Fatalf("Resolve(%#v) = %#v, want no claim", tt.input, got)
				}
				return
			}
			if got == nil || got.Kind != tt.kind {
				t.Fatalf("Resolve(%#v) = %#v, want %s", tt.input, got, tt.kind)
			}
			if tt.unsetTV && (got.IndexNumber != nil || got.ParentIndexNumber != nil) {
				t.Fatalf("unparsed TV video = %#v, want episode with unset season and episode indices", got)
			}
		})
	}
}

func TestDefaultMovieMultiResolverHandlesSourceCollectionKinds(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	for _, tt := range []struct {
		name       string
		collection item.CollectionType
		kind       item.Kind
	}{
		{"home videos", item.CollectionTypeHomeVideos, item.KindVideo},
		{"mixed collection", item.CollectionTypeUnknown, item.KindVideo},
		{"music videos", item.CollectionTypeMusicVideos, item.KindMusicVideo},
		{"TV shows", item.CollectionTypeTVShows, item.KindEpisode},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.ResolveMany(t.Context(), library.ResolveContext{
				CollectionType: tt.collection,
				Children:       []library.Entry{{Path: "/missing/collection/Video.mkv", Size: 42}},
			})
			if err != nil {
				t.Fatalf("ResolveMany() error = %v", err)
			}
			if len(got) != 1 || got[0] == nil || got[0].Kind != tt.kind {
				t.Fatalf("ResolveMany(%s) = %#v, want one %s", tt.collection, got, tt.kind)
			}
		})
	}
}

func TestDefaultResolversInitializeSourceIdentityAndEntryFactsWithoutExternalWork(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xa1}, TopParentID: &dto.UUID{0xa2}, Kind: item.KindFolder}
	created := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	modified := time.Date(2026, time.July, 26, 14, 30, 0, 0, time.UTC)
	input := library.ResolveContext{
		Entry:           library.Entry{Path: "/path/that/does/not/exist/movies/Odd Names/Café déjà-vu (2020).mkv", Size: 987654, Created: created, Modified: modified},
		Parent:          &parent,
		CollectionType:  item.CollectionTypeMovies,
		ProgramDataRoot: "/path/that/does/not/exist",
	}

	got, err := chain.Resolve(t.Context(), input)
	if err != nil {
		t.Fatalf("Resolve(non-existent entry facts) error = %v", err)
	}
	if got == nil || got.Kind != item.KindMovie || got.Name == nil || *got.Name != "Café déjà-vu" || got.Path == nil || *got.Path != input.Entry.Path || got.Size == nil || *got.Size != input.Entry.Size || got.DateCreated == nil || !got.DateCreated.Equal(created) || got.DateModified == nil || !got.DateModified.Equal(modified) || got.ParentID == nil || *got.ParentID != parent.ID || got.TopParentID == nil || *got.TopParentID != *parent.TopParentID {
		t.Fatalf("initialized movie = %#v, want source name plus supplied path/date/size/parent facts", got)
	}

	unclean := input
	unclean.Entry.Path = "/path/that/does/not/exist/movies/Odd Names/../Odd Names/Café déjà-vu (2020).mkv"
	uncleanGot, err := chain.Resolve(t.Context(), unclean)
	if err != nil {
		t.Fatalf("Resolve(unclean source path) error = %v", err)
	}
	if uncleanGot == nil || uncleanGot.ID == got.ID {
		t.Fatalf("distinct source keys with and without ../ produced IDs %v and %v, want source path text retained", got.ID, uncleanGot)
	}

	asVideo := input
	asVideo.CollectionType = item.CollectionTypeHomeVideos
	video, err := chain.Resolve(t.Context(), asVideo)
	if err != nil {
		t.Fatalf("Resolve(home-video source) error = %v", err)
	}
	if video == nil || video.Kind != item.KindVideo || video.ID == got.ID {
		t.Fatalf("same path with Video rather than Movie = %#v, want distinct type-derived identity from %#v", video, got)
	}
}

func TestDefaultResolversUseSourceProgramDataAndCaseIdentityRules(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	programData := "/var/lib/jellyfin/data"
	for _, tt := range []struct {
		name          string
		path          string
		caseSensitive bool
		wantID        dto.UUID
	}{
		{"program data path is made portable", "/var/lib/jellyfin/data/metadata/Movie.mkv", false, dto.UUID{0xfa, 0x05, 0x35, 0x19, 0xa7, 0xb3, 0x2a, 0x71, 0xcd, 0x7f, 0x39, 0x51, 0x1c, 0x02, 0x12, 0x69}},
		{"case insensitive IDs lowercase by default", "/var/lib/jellyfin/data/metadata/Case.mkv", false, dto.UUID{0x65, 0x9a, 0x7c, 0x90, 0x4d, 0xca, 0x49, 0x88, 0x31, 0x97, 0x2e, 0x07, 0x94, 0x87, 0x37, 0xa8}},
		{"case sensitive IDs retain source path casing", "/var/lib/jellyfin/data/metadata/Case.mkv", true, dto.UUID{0x64, 0x03, 0xc0, 0xa7, 0x10, 0x7e, 0x9b, 0x97, 0x87, 0x35, 0x71, 0xfd, 0xdf, 0xf3, 0x17, 0xe1}},
		{"case insensitive IDs use .NET invariant casing for dotted capital I", "/var/lib/jellyfin/data/metadata/İ.mkv", false, dto.UUID{0xd1, 0x74, 0x03, 0xbd, 0x04, 0x53, 0xe1, 0xc2, 0xd3, 0x3e, 0x21, 0x8e, 0xd7, 0xde, 0x28, 0x3a}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), library.ResolveContext{
				Entry:            library.Entry{Path: tt.path},
				CollectionType:   item.CollectionTypeMovies,
				ProgramDataRoot:  programData,
				CaseSensitiveIDs: tt.caseSensitive,
			})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.path, err)
			}
			if got == nil || got.ID != tt.wantID {
				t.Fatalf("Resolve(%q).ID = %v, want source ID %v", tt.path, got, tt.wantID)
			}
		})
	}
}

func TestDefaultResolversInitializeLockState(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	for _, tt := range []struct {
		name   string
		path   string
		parent *item.Item
	}{
		{"dont fetch metadata marker", "/missing/movies/Locked [dontfetchmeta].mkv", nil},
		{"locked parent", "/missing/movies/Child.mkv", &item.Item{ID: dto.UUID{0xa3}, Kind: item.KindFolder, IsLocked: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: library.Entry{Path: tt.path}, Parent: tt.parent, CollectionType: item.CollectionTypeMovies})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got == nil || !got.IsLocked {
				t.Fatalf("Resolve(%q) = %#v, want initialized locked item", tt.path, got)
			}
		})
	}
}

type resolverProbe struct {
	priority library.ResolverPriority
	resolve  func(context.Context, library.ResolveContext) (*item.Item, error)
}

func (r resolverProbe) Priority() library.ResolverPriority { return r.priority }
func (r resolverProbe) Resolve(ctx context.Context, input library.ResolveContext) (*item.Item, error) {
	return r.resolve(ctx, input)
}

type multiResolverProbe struct {
	resolverProbe
	resolveMany func(context.Context, library.ResolveContext) ([]*item.Item, error)
}

func (r multiResolverProbe) ResolveMany(ctx context.Context, input library.ResolveContext) ([]*item.Item, error) {
	return r.resolveMany(ctx, input)
}

func itemWithName(kind item.Kind, name string) *item.Item {
	return &item.Item{Kind: kind, Name: &name}
}

func resolverPriorities(resolvers []library.Resolver) []library.ResolverPriority {
	priorities := make([]library.ResolverPriority, len(resolvers))
	for index, resolver := range resolvers {
		priorities[index] = resolver.Priority()
	}
	return priorities
}

// TestResolverTopParentStopsAtCollectionFolder proves that TopParentID stops at
// the library boundary. The CollectionFolder (parent = AggregateFolder) is the
// top parent, per upstream BaseItem.IsTopParent. Descendants inherit the
// CollectionFolder ID, never the AggregateFolder ID. Before the fix, every
// item inherited the AggregateFolder ID and TopParentIDs queries returned zero
// rows.
func TestResolverTopParentStopsAtCollectionFolder(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	ctx := t.Context()

	// Resolve the AggregateFolder (the application root, no parent).
	aggregate, err := chain.Resolve(ctx, library.ResolveContext{
		Entry:           library.Entry{Path: "/missing/root", IsDir: true},
		PhysicalRoot:    true,
		ProgramDataRoot: "/missing",
	})
	if err != nil {
		t.Fatalf("Resolve(aggregate) error = %v", err)
	}
	if aggregate == nil || aggregate.Kind != item.KindAggregateFolder {
		t.Fatalf("Resolve(aggregate) = %#v, want AggregateFolder", aggregate)
	}
	if aggregate.TopParentID != nil {
		t.Fatalf("AggregateFolder TopParentID = %v, want nil", *aggregate.TopParentID)
	}

	// Resolve the CollectionFolder under the AggregateFolder (the library root).
	collection, err := chain.Resolve(ctx, library.ResolveContext{
		Entry:           library.Entry{Path: "/missing/Movies", IsDir: true},
		Parent:          aggregate,
		IsVirtualFolder: true,
		CollectionType:  item.CollectionTypeMovies,
		ProgramDataRoot: "/missing",
	})
	if err != nil {
		t.Fatalf("Resolve(collection) error = %v", err)
	}
	if collection == nil || collection.Kind != item.KindCollectionFolder {
		t.Fatalf("Resolve(collection) = %#v, want CollectionFolder", collection)
	}
	if collection.TopParentID == nil {
		t.Fatalf("CollectionFolder TopParentID is nil, want its own ID")
	}
	if *collection.TopParentID != collection.ID {
		t.Fatalf("CollectionFolder TopParentID = %v, want its own ID %v", *collection.TopParentID, collection.ID)
	}

	// Resolve a physical library source Folder under the CollectionFolder.
	source, err := chain.Resolve(ctx, library.ResolveContext{
		Entry:                 library.Entry{Path: "/missing/Movies/Source", IsDir: true},
		Parent:                collection,
		IsPhysicalLibraryRoot: true,
		CollectionType:        item.CollectionTypeMovies,
		ProgramDataRoot:       "/missing",
	})
	if err != nil {
		t.Fatalf("Resolve(source) error = %v", err)
	}
	if source == nil || source.Kind != item.KindFolder {
		t.Fatalf("Resolve(source) = %#v, want Folder", source)
	}
	if source.TopParentID == nil {
		t.Fatalf("source Folder TopParentID is nil, want CollectionFolder ID")
	}
	if *source.TopParentID != collection.ID {
		t.Fatalf("source Folder TopParentID = %v, want CollectionFolder ID %v", *source.TopParentID, collection.ID)
	}

	// Resolve a Movie under the source Folder. It must inherit the CollectionFolder ID.
	movie, err := chain.Resolve(ctx, library.ResolveContext{
		Entry:           library.Entry{Path: "/missing/Movies/Source/Film (2024).mkv", Size: 42},
		Parent:          source,
		CollectionType:  item.CollectionTypeMovies,
		ProgramDataRoot: "/missing",
	})
	if err != nil {
		t.Fatalf("Resolve(movie) error = %v", err)
	}
	if movie == nil || movie.Kind != item.KindMovie {
		t.Fatalf("Resolve(movie) = %#v, want Movie", movie)
	}
	if movie.TopParentID == nil {
		t.Fatalf("Movie TopParentID is nil, want CollectionFolder ID")
	}
	if *movie.TopParentID != collection.ID {
		t.Fatalf("Movie TopParentID = %v, want CollectionFolder ID %v", *movie.TopParentID, collection.ID)
	}

	// The AggregateFolder ID must never appear as a descendant TopParentID.
	if movie.TopParentID != nil && *movie.TopParentID == aggregate.ID {
		t.Fatalf("Movie TopParentID points at AggregateFolder %v, want CollectionFolder %v", aggregate.ID, collection.ID)
	}
}

// TestSeasonResolverStripsSeriesFolderName follows upstream SeasonResolver,
// which parses the folder with the series folder as the parent path and
// names a numbered season "Season N".
func TestSeasonResolverStripsSeriesFolderName(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	seriesName := "Archer"
	for _, tt := range []struct {
		series, folder string
		index          int
		name           string
	}{
		{"/missing/tv/Archer", "Archer Season 1  (1080p H265 Joy)", 1, "Season 1"},
		{"/missing/tv/Game.Of.Thrones", "Game Of Thrones Season 2 - Complete", 2, "Season 2"},
		{"/missing/tv/Archer", "Archer.2009.SEASON.08.S08.COMPLETE.1080p.WEB-DL", 8, "Season 8"},
		{"/missing/tv/Archer", "Specials", 0, "Specials"},
	} {
		t.Run(tt.folder, func(t *testing.T) {
			seriesPath := tt.series
			series := item.Item{ID: dto.UUID{0xf6}, Kind: item.KindSeries, Name: &seriesName, Path: &seriesPath}
			got, err := chain.Resolve(t.Context(), library.ResolveContext{
				Entry:          library.Entry{Path: tt.series + "/" + tt.folder, IsDir: true},
				Parent:         &series,
				CollectionType: item.CollectionTypeTVShows,
				Options:        library.LibraryOptions{SeasonZeroDisplayName: "Specials"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Kind != item.KindSeason || got.IndexNumber == nil || *got.IndexNumber != tt.index || got.Name == nil || *got.Name != tt.name {
				t.Fatalf("Resolve(%q) = %#v, want season %d %q", tt.folder, got, tt.index, tt.name)
			}
		})
	}
}

// TestDefaultResolversTreatDiscFoldersAsPartOfTheAlbum ports
// MusicAlbumResolver.ContainsMusic and the multi-disc skip of
// MusicArtistResolver (#703). An album folder whose tracks are in disc
// folders ("CD 01", "Disc 1", "Digital Media 01") is the album, and its
// parent is the artist.
func TestDefaultResolversTreatDiscFoldersAsPartOfTheAlbum(t *testing.T) {
	chain := library.NewDefaultChain(naming.DefaultOptions())
	parent := item.Item{ID: dto.UUID{0xf9}, Kind: item.KindFolder}
	artistParent := item.Item{ID: dto.UUID{0xfa}, Kind: item.KindMusicArtist}
	albumParent := item.Item{ID: dto.UUID{0xfb}, Kind: item.KindMusicAlbum}
	const root = "/missing/music"
	dir := func(path string) library.Entry { return library.Entry{Path: root + "/" + path, IsDir: true} }
	file := func(path string) library.Entry { return library.Entry{Path: root + "/" + path, Size: 1} }
	for _, discs := range [][]string{{"CD 01", "CD 02"}, {"Disc 1", "Disc 2"}, {"Digital Media 01", "Digital Media 02", "Digital Media 03"}, {"cd1"}, {"Disc 04 (Encores and Folk Songs)"}} {
		album := "Artist/Album (2008)"
		children := make([]library.Entry, 0, len(discs)+1)
		childEntries := map[string][]library.Entry{}
		for _, disc := range discs {
			children = append(children, dir(album+"/"+disc))
			childEntries[root+"/"+album+"/"+disc] = []library.Entry{file(album + "/" + disc + "/01 - Track.flac")}
		}
		children = append(children, file(album+"/cover.jpg"))
		childEntries[root+"/"+album] = children
		for _, tt := range []struct {
			name  string
			input library.ResolveContext
			kind  item.Kind
		}{
			{"artist", library.ResolveContext{Entry: dir("Artist"), Parent: &parent, Children: []library.Entry{dir(album)}, ChildEntries: childEntries, CollectionType: item.CollectionTypeMusic}, item.KindMusicArtist},
			{"album under artist", library.ResolveContext{Entry: dir(album), Parent: &artistParent, Children: children, ChildEntries: childEntries, CollectionType: item.CollectionTypeMusic}, item.KindMusicAlbum},
			{"album without artist", library.ResolveContext{Entry: dir(album), Parent: &parent, Children: children, ChildEntries: childEntries, CollectionType: item.CollectionTypeMusic}, item.KindMusicAlbum},
			{"disc folder", library.ResolveContext{Entry: children[0], Parent: &albumParent, Children: childEntries[children[0].Path], ChildEntries: childEntries, CollectionType: item.CollectionTypeMusic}, item.KindFolder},
		} {
			t.Run(discs[0]+"/"+tt.name, func(t *testing.T) {
				got, err := chain.Resolve(t.Context(), tt.input)
				if err != nil {
					t.Fatalf("Resolve(%s) error = %v", tt.input.Entry.Path, err)
				}
				if got == nil || got.Kind != tt.kind {
					t.Fatalf("Resolve(%s) = %#v, want %s", tt.input.Entry.Path, got, tt.kind)
				}
			})
		}
	}

	// A subfolder with music that is not a disc folder makes the folder a
	// collection of albums, not a multi-disc album (ContainsMusic stops).
	mixed := map[string][]library.Entry{
		root + "/Compilations/Best Of/CD 01": {file("Compilations/Best Of/CD 01/01 - Track.flac")},
		root + "/Compilations/Best Of/Live":  {file("Compilations/Best Of/Live/01 - Track.flac")},
	}
	mixedChildren := []library.Entry{dir("Compilations/Best Of/CD 01"), dir("Compilations/Best Of/Live")}
	got, err := chain.Resolve(t.Context(), library.ResolveContext{Entry: dir("Compilations/Best Of"), Parent: &artistParent, Children: mixedChildren, ChildEntries: mixed, CollectionType: item.CollectionTypeMusic})
	if err != nil || got == nil || got.Kind != item.KindFolder {
		t.Fatalf("Resolve(disc and non-disc subfolders under an artist) = %#v, %v; want Folder", got, err)
	}
	// An empty disc folder holds no music.
	empty := map[string][]library.Entry{root + "/Artist/Empty/CD 01": nil}
	got, err = chain.Resolve(t.Context(), library.ResolveContext{Entry: dir("Artist/Empty"), Parent: &artistParent, Children: []library.Entry{dir("Artist/Empty/CD 01")}, ChildEntries: empty, CollectionType: item.CollectionTypeMusic})
	if err != nil || got == nil || got.Kind != item.KindFolder {
		t.Fatalf("Resolve(empty disc folder) = %#v, %v; want Folder", got, err)
	}
}
