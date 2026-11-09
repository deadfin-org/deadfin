// SPDX-License-Identifier: GPL-2.0-only

package dtobuild_test

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

func parentTestImage(imageType item.ImageType, index int, path, blurHash string) item.ImageInfo {
	image := item.ImageInfo{Type: imageType, Index: index, Path: path, Width: 1000, Height: 1500}
	if blurHash != "" {
		image.BlurHash = &blurHash
	}
	return image
}

type parentImageLibrary struct {
	collection, series, seasonWithPoster, seasonWithout dto.UUID
	artist, album, disc                                 dto.UUID
	episodes                                            []dto.UUID
	track, discTrack, trackWithCover                    dto.UUID
}

// seedParentImageLibrary stores a TV and a music tree under one collection
// folder. The series has every image type; one season has its own poster.
// The artist folder has a backdrop and a logo; the album has a cover.
func seedParentImageLibrary(t *testing.T, items *store.ItemStore, episodeCount int) parentImageLibrary {
	t.Helper()
	library := parentImageLibrary{
		collection: builderUUID(t, 0x1000), series: builderUUID(t, 0x1001), seasonWithPoster: builderUUID(t, 0x1002), seasonWithout: builderUUID(t, 0x1003),
		artist: builderUUID(t, 0x1004), album: builderUUID(t, 0x1005), disc: builderUUID(t, 0x1006),
		track: builderUUID(t, 0x1007), discTrack: builderUUID(t, 0x1008), trackWithCover: builderUUID(t, 0x1009),
	}
	name := func(value string) *string { return &value }
	values := []item.Item{
		{ID: library.collection, Kind: item.KindCollectionFolder, IsFolder: true, Name: name("Library"),
			ImageInfos: []item.ImageInfo{parentTestImage(item.ImageTypeThumb, 0, "/meta/library/thumb.jpg", "")}},
		{ID: library.series, Kind: item.KindSeries, IsFolder: true, ParentID: &library.collection, Name: name("Show"), ImageInfos: []item.ImageInfo{
			parentTestImage(item.ImageTypePrimary, 0, "/show/poster.jpg", "SERIESPOSTER"),
			parentTestImage(item.ImageTypeBackdrop, 0, "/show/backdrop.jpg", "SERIESBACKDROP"),
			parentTestImage(item.ImageTypeBackdrop, 1, "/show/backdrop1.jpg", ""),
			parentTestImage(item.ImageTypeLogo, 0, "/show/logo.png", ""),
			parentTestImage(item.ImageTypeThumb, 0, "/show/landscape.jpg", "SERIESTHUMB"),
		}},
		{ID: library.seasonWithPoster, Kind: item.KindSeason, IsFolder: true, ParentID: &library.series, SeriesID: &library.series, Name: name("Season 1"),
			ImageInfos: []item.ImageInfo{parentTestImage(item.ImageTypePrimary, 0, "/show/season01-poster.jpg", "SEASONPOSTER")}},
		{ID: library.seasonWithout, Kind: item.KindSeason, IsFolder: true, ParentID: &library.series, SeriesID: &library.series, Name: name("Season 2")},
		{ID: library.artist, Kind: item.KindMusicArtist, IsFolder: true, ParentID: &library.collection, Name: name("Artist"), ImageInfos: []item.ImageInfo{
			parentTestImage(item.ImageTypeBackdrop, 0, "/music/artist/backdrop.jpg", ""),
			parentTestImage(item.ImageTypeLogo, 0, "/music/artist/logo.png", ""),
		}},
		{ID: library.album, Kind: item.KindMusicAlbum, IsFolder: true, ParentID: &library.artist, Name: name("Album"),
			ImageInfos: []item.ImageInfo{parentTestImage(item.ImageTypePrimary, 0, "/music/artist/album/cover.jpg", "ALBUMCOVER")}},
		{ID: library.disc, Kind: item.KindFolder, IsFolder: true, ParentID: &library.album, Name: name("CD 2")},
		{ID: library.track, Kind: item.KindAudio, ParentID: &library.album, Name: name("Track")},
		{ID: library.discTrack, Kind: item.KindAudio, ParentID: &library.disc, Name: name("Disc track")},
		{ID: library.trackWithCover, Kind: item.KindAudio, ParentID: &library.album, Name: name("Covered track"),
			ImageInfos: []item.ImageInfo{parentTestImage(item.ImageTypePrimary, 0, "/music/artist/album/track.jpg", "")}},
	}
	for index := range episodeCount {
		id := builderUUID(t, 0x2000+index)
		library.episodes = append(library.episodes, id)
		season := library.seasonWithPoster
		if index%2 == 1 {
			season = library.seasonWithout
		}
		values = append(values, item.Item{ID: id, Kind: item.KindEpisode, ParentID: &season, SeasonID: &season, SeriesID: &library.series, Name: name("Episode"),
			ImageInfos: []item.ImageInfo{parentTestImage(item.ImageTypePrimary, 0, "/show/episode.jpg", "")}})
	}
	for _, value := range values {
		if err := items.Upsert(t.Context(), value); err != nil {
			t.Fatalf("upsert %s %s: %v", value.Kind, value.ID, err)
		}
	}
	return library
}

func parentTestBuild(t *testing.T, items *store.ItemStore, ids []dto.UUID, options store.ItemQueryDTOOptions) map[dto.UUID]dto.BaseItemDto {
	t.Helper()
	values := make([]item.Item, len(ids))
	for index, id := range ids {
		values[index] = builderProjectionLoad(t, items, id)
	}
	result, err := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil)).Build(t.Context(), values, options, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	byID := make(map[dto.UUID]dto.BaseItemDto, len(result.Items))
	for _, value := range result.Items {
		byID[value.ID] = value
	}
	return byID
}

func uuidValue(value *dto.UUID) dto.UUID {
	if value == nil {
		return dto.NilUUID
	}
	return *value
}

// TestBuilderProjectsParentImagesAsDtoService covers the episode, season and
// audio fields of DtoService.AttachBasicFields and AddInheritedImages
// (deadfin#903).
func TestBuilderProjectsParentImagesAsDtoService(t *testing.T) {
	items, _ := openTaxonomyCountingItemStore(t)
	library := seedParentImageLibrary(t, items, 2)
	all := store.ItemQueryDTOOptions{EnableImages: true, ImageTypeLimit: 1 << 30}
	ids := []dto.UUID{library.episodes[0], library.episodes[1], library.seasonWithPoster, library.seasonWithout, library.track, library.discTrack, library.trackWithCover, library.album}
	byID := parentTestBuild(t, items, ids, all)

	inSeasonOne := byID[library.episodes[0]]
	if got, want := inSeasonOne.SeriesPrimaryImageTag, "poster.jpg"; got != want {
		t.Errorf("episode SeriesPrimaryImageTag = %q, want %q", got, want)
	}
	if got, want := uuidValue(inSeasonOne.ParentPrimaryImageItemID), library.seasonWithPoster; got != want {
		t.Errorf("episode ParentPrimaryImageItemId = %s, want season %s", got, want)
	}
	if got, want := inSeasonOne.ParentPrimaryImageTag, "season01-poster.jpg"; got != want {
		t.Errorf("episode ParentPrimaryImageTag = %q, want %q", got, want)
	}
	if got, want := uuidValue(inSeasonOne.ParentBackdropItemID), library.series; got != want {
		t.Errorf("episode ParentBackdropItemId = %s, want series %s", got, want)
	}
	if got, want := inSeasonOne.ParentBackdropImageTags, []string{"backdrop.jpg", "backdrop1.jpg"}; !equalStrings(got, want) {
		t.Errorf("episode ParentBackdropImageTags = %v, want %v", got, want)
	}
	if got, want := uuidValue(inSeasonOne.ParentLogoItemID), library.series; got != want || inSeasonOne.ParentLogoImageTag != "logo.png" {
		t.Errorf("episode ParentLogo = %s %q, want series logo.png", got, inSeasonOne.ParentLogoImageTag)
	}
	if got, want := uuidValue(inSeasonOne.ParentThumbItemID), library.series; got != want || inSeasonOne.ParentThumbImageTag != "landscape.jpg" {
		t.Errorf("episode ParentThumb = %s %q, want series landscape.jpg", got, inSeasonOne.ParentThumbImageTag)
	}
	for imageType, tag := range map[string]string{"Primary": "poster.jpg", "Backdrop": "backdrop.jpg", "Thumb": "landscape.jpg"} {
		if _, found := inSeasonOne.ImageBlurHashes[imageType][tag]; !found {
			t.Errorf("episode ImageBlurHashes[%s] lacks %s: %v", imageType, tag, inSeasonOne.ImageBlurHashes)
		}
	}
	if got := inSeasonOne.ImageBlurHashes["Primary"]["season01-poster.jpg"]; got != "SEASONPOSTER" {
		t.Errorf("episode season poster BlurHash = %q", got)
	}

	inSeasonTwo := byID[library.episodes[1]]
	if got, want := uuidValue(inSeasonTwo.ParentPrimaryImageItemID), library.series; got != want || inSeasonTwo.ParentPrimaryImageTag != "poster.jpg" {
		t.Errorf("episode without season poster ParentPrimaryImage = %s %q, want series poster.jpg", got, inSeasonTwo.ParentPrimaryImageTag)
	}

	for _, id := range []dto.UUID{library.seasonWithPoster, library.seasonWithout} {
		season := byID[id]
		if got, want := season.SeriesPrimaryImageTag, "poster.jpg"; got != want {
			t.Errorf("season %s SeriesPrimaryImageTag = %q, want %q", id, got, want)
		}
		if got, want := uuidValue(season.ParentBackdropItemID), library.series; got != want {
			t.Errorf("season %s ParentBackdropItemId = %s, want %s", id, got, want)
		}
		if season.ParentPrimaryImageItemID != nil {
			t.Errorf("season %s ParentPrimaryImageItemId = %s, want absent", id, season.ParentPrimaryImageItemID)
		}
	}
	if ratio := byID[library.seasonWithout].PrimaryImageAspectRatio; ratio == nil || *ratio != 1000.0/1500.0 {
		t.Errorf("season without poster PrimaryImageAspectRatio = %v, want series ratio", ratio)
	}

	for _, id := range []dto.UUID{library.track, library.discTrack, library.trackWithCover} {
		track := byID[id]
		if got, want := track.AlbumPrimaryImageTag, "cover.jpg"; got != want {
			t.Errorf("track %s AlbumPrimaryImageTag = %q, want %q", id, got, want)
		}
		if got, want := uuidValue(track.AlbumID), library.album; got != want {
			t.Errorf("track %s AlbumId = %s, want %s", id, got, want)
		}
		if got, want := uuidValue(track.ParentBackdropItemID), library.artist; got != want {
			t.Errorf("track %s ParentBackdropItemId = %s, want artist %s", id, got, want)
		}
		if got, want := uuidValue(track.ParentLogoItemID), library.artist; got != want {
			t.Errorf("track %s ParentLogoItemId = %s, want artist %s", id, got, want)
		}
	}
	album := byID[library.album]
	if got, want := uuidValue(album.ParentBackdropItemID), library.artist; got != want {
		t.Errorf("album ParentBackdropItemId = %s, want artist %s", got, want)
	}
}

// TestBuilderParentImagesFollowImageOptions checks EnableImages,
// EnableImageTypes and ImageTypeLimit as DtoOptions.GetImageLimit reads them.
func TestBuilderParentImagesFollowImageOptions(t *testing.T) {
	items, _ := openTaxonomyCountingItemStore(t)
	library := seedParentImageLibrary(t, items, 1)
	episode := library.episodes[0]

	disabled := parentTestBuild(t, items, []dto.UUID{episode, library.track}, store.ItemQueryDTOOptions{})[episode]
	if got, want := disabled.SeriesPrimaryImageTag, "poster.jpg"; got != want {
		t.Errorf("EnableImages=false SeriesPrimaryImageTag = %q, want %q (upstream sets it without images)", got, want)
	}
	if disabled.ParentPrimaryImageItemID != nil || disabled.ParentBackdropItemID != nil || disabled.ParentLogoItemID != nil || disabled.ParentThumbItemID != nil {
		t.Errorf("EnableImages=false projected parent images: %+v", disabled)
	}

	primaryOnly := parentTestBuild(t, items, []dto.UUID{episode}, store.ItemQueryDTOOptions{EnableImages: true, ImageTypes: []item.ImageType{item.ImageTypePrimary}, ImageTypeLimit: 1})[episode]
	if primaryOnly.ParentPrimaryImageItemID == nil || primaryOnly.ParentBackdropItemID != nil || primaryOnly.ParentLogoItemID != nil {
		t.Errorf("Primary-only projection = parent primary %v, backdrop %v, logo %v", primaryOnly.ParentPrimaryImageItemID, primaryOnly.ParentBackdropItemID, primaryOnly.ParentLogoItemID)
	}

	// AddInheritedImages walks only while a Logo, Art or Thumb is missing;
	// a Backdrop-only request does not start the walk.
	backdropOnly := parentTestBuild(t, items, []dto.UUID{episode}, store.ItemQueryDTOOptions{EnableImages: true, ImageTypes: []item.ImageType{item.ImageTypeBackdrop}, ImageTypeLimit: 1})[episode]
	if backdropOnly.ParentBackdropItemID != nil {
		t.Errorf("Backdrop-only projection ParentBackdropItemId = %s, want absent", backdropOnly.ParentBackdropItemID)
	}

	swiftfin := parentTestBuild(t, items, []dto.UUID{episode}, store.ItemQueryDTOOptions{EnableImages: true, ImageTypes: []item.ImageType{item.ImageTypePrimary, item.ImageTypeBackdrop, item.ImageTypeThumb, item.ImageTypeLogo, item.ImageTypeBanner}, ImageTypeLimit: 1})[episode]
	if got, want := swiftfin.ParentBackdropImageTags, []string{"backdrop.jpg"}; !equalStrings(got, want) {
		t.Errorf("ImageTypeLimit=1 ParentBackdropImageTags = %v, want %v", got, want)
	}
}

// TestBuilderParentImagesQueriesFollowTreeDepth requires the parent lookups
// to stay a fixed number of batches for any page size.
func TestBuilderParentImagesQueriesFollowTreeDepth(t *testing.T) {
	items, counting := openTaxonomyCountingItemStore(t)
	library := seedParentImageLibrary(t, items, 60)
	options := store.ItemQueryDTOOptions{EnableImages: true, ImageTypeLimit: 1}
	count := func(ids []dto.UUID) int64 {
		values := make([]item.Item, len(ids))
		for index, id := range ids {
			values[index] = builderProjectionLoad(t, items, id)
		}
		builder := dtobuild.NewBuilder(items, dtobuild.NewStoreEnricher(items, nil, nil))
		counting.ResetReaderQueries()
		if _, err := builder.Build(t.Context(), values, options, nil); err != nil {
			t.Fatalf("build: %v", err)
		}
		return counting.ReaderQueries()
	}
	small, large := count(library.episodes[:2]), count(library.episodes)
	t.Logf("reader queries: 2 episodes %d, 60 episodes %d", small, large)
	if small != large {
		t.Errorf("reader queries for 2 episodes = %d, for 60 episodes = %d; want the same", small, large)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
