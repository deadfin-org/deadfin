// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// Sources: docs/02-storage.md:47-105 (one typed column or normalized relation
// per queryable fact); upstream/jellyfin/MediaBrowser.Controller/Entities/
// BaseItemEntity.cs:10-165; BaseItemMapper.cs:44-114,217-354; and
// InternalItemsQuery.cs:15-497. These fields are the aggregate's public
// persistence/query seam, so nullable database facts must remain pointers.
func TestItemExposesCanonicalPersistedAndQueryableFields(t *testing.T) {
	assertExactFieldTypes(t, reflect.TypeOf(Item{}), []fieldType{
		{"ID", reflect.TypeOf(dto.UUID{})},
		{"Kind", reflect.TypeOf(Kind(0))},
		{"ParentID", reflect.TypeOf((*dto.UUID)(nil))},
		{"TopParentID", reflect.TypeOf((*dto.UUID)(nil))},
		{"SeriesID", reflect.TypeOf((*dto.UUID)(nil))},
		{"SeasonID", reflect.TypeOf((*dto.UUID)(nil))},
		{"ChannelID", reflect.TypeOf((*dto.UUID)(nil))},
		{"PrimaryVersionID", reflect.TypeOf((*dto.UUID)(nil))},
		{"OwnerID", reflect.TypeOf((*dto.UUID)(nil))},
		{"PlaylistOwnerUserID", reflect.TypeOf(dto.UUID{})},
		{"Path", reflect.TypeOf((*string)(nil))},
		{"Name", reflect.TypeOf((*string)(nil))},
		{"SortName", reflect.TypeOf((*string)(nil))},
		{"CleanName", reflect.TypeOf((*string)(nil))},
		{"ForcedSortName", reflect.TypeOf((*string)(nil))},
		{"OriginalTitle", reflect.TypeOf((*string)(nil))},
		{"OriginalLanguage", reflect.TypeOf((*string)(nil))},
		{"Overview", reflect.TypeOf((*string)(nil))},
		{"Tagline", reflect.TypeOf((*string)(nil))},
		{"OfficialRating", reflect.TypeOf((*string)(nil))},
		{"CustomRating", reflect.TypeOf((*string)(nil))},
		{"PresentationUniqueKey", reflect.TypeOf((*string)(nil))},
		{"SeriesPresentationUniqueKey", reflect.TypeOf((*string)(nil))},
		{"Album", reflect.TypeOf((*string)(nil))},
		{"ExternalID", reflect.TypeOf((*string)(nil))},
		{"ExternalSeriesID", reflect.TypeOf((*string)(nil))},
		{"ExternalServiceID", reflect.TypeOf((*string)(nil))},
		{"PreferredMetadataLanguage", reflect.TypeOf((*string)(nil))},
		{"PreferredMetadataCountryCode", reflect.TypeOf((*string)(nil))},
		{"SeriesName", reflect.TypeOf((*string)(nil))},
		{"SeasonName", reflect.TypeOf((*string)(nil))},
		{"EpisodeTitle", reflect.TypeOf((*string)(nil))},
		{"ShowID", reflect.TypeOf((*string)(nil))},
		{"Container", reflect.TypeOf((*string)(nil))},
		// MediaSourceContainer persists source-only container state (for example,
		// an optical disc) without exposing a duplicate client Item.Container field.
		{"MediaSourceContainer", reflect.TypeOf((*string)(nil))},
		{"StartDate", reflect.TypeOf((*time.Time)(nil))},
		{"EndDate", reflect.TypeOf((*time.Time)(nil))},
		{"PremiereDate", reflect.TypeOf((*time.Time)(nil))},
		{"DateCreated", reflect.TypeOf((*time.Time)(nil))},
		{"DateModified", reflect.TypeOf((*time.Time)(nil))},
		{"FileChangedAt", reflect.TypeOf((*time.Time)(nil))},
		{"DateLastRefreshed", reflect.TypeOf((*time.Time)(nil))},
		{"DateLastSaved", reflect.TypeOf((*time.Time)(nil))},
		{"DateLastMediaAdded", reflect.TypeOf((*time.Time)(nil))},
		{"ProductionYear", reflect.TypeOf((*int)(nil))},
		{"IndexNumber", reflect.TypeOf((*int)(nil))},
		{"ParentIndexNumber", reflect.TypeOf((*int)(nil))},
		{"Width", reflect.TypeOf((*int)(nil))},
		{"Height", reflect.TypeOf((*int)(nil))},
		{"TotalBitrate", reflect.TypeOf((*int)(nil))},
		{"InheritedParentalRatingValue", reflect.TypeOf((*int)(nil))},
		{"InheritedParentalRatingSubValue", reflect.TypeOf((*int)(nil))},
		{"AirsBeforeSeasonNumber", reflect.TypeOf((*int)(nil))},
		{"AirsAfterSeasonNumber", reflect.TypeOf((*int)(nil))},
		{"AirsBeforeEpisodeNumber", reflect.TypeOf((*int)(nil))},
		{"RunTimeTicks", reflect.TypeOf((*int64)(nil))},
		{"Size", reflect.TypeOf((*int64)(nil))},
		{"FileDevice", reflect.TypeOf((*int64)(nil))},
		{"FileInode", reflect.TypeOf((*int64)(nil))},
		{"CommunityRating", reflect.TypeOf((*float32)(nil))},
		{"CriticRating", reflect.TypeOf((*float32)(nil))},
		{"LUFS", reflect.TypeOf((*float32)(nil))},
		{"NormalizationGain", reflect.TypeOf((*float32)(nil))},
		{"UnratedType", reflect.TypeOf((*UnratedItem)(nil))},
		{"SourceType", reflect.TypeOf(SourceType(0))},
		{"MediaType", reflect.TypeOf((*MediaType)(nil))},
		{"ExtraType", reflect.TypeOf((*ExtraType)(nil))},
		{"VideoType", reflect.TypeOf((*VideoType)(nil))},
		{"IsoType", reflect.TypeOf((*IsoType)(nil))},
		{"Video3DFormat", reflect.TypeOf((*Video3DFormat)(nil))},
		{"SeriesStatus", reflect.TypeOf((*SeriesStatus)(nil))},
		{"ProgramAudio", reflect.TypeOf((*ProgramAudio)(nil))},
		{"IsFolder", reflect.TypeOf(false)},
		{"IsVirtualItem", reflect.TypeOf(false)},
		{"IsInMixedFolder", reflect.TypeOf(false)},
		{"IsLocked", reflect.TypeOf(false)},
		{"IsMovie", reflect.TypeOf(false)},
		{"IsSeries", reflect.TypeOf(false)},
		{"IsRepeat", reflect.TypeOf(false)},
		{"IsPlaceHolder", reflect.TypeOf(false)},
		{"ProviderIDs", reflect.TypeOf(ProviderIDs{})},
		{"ImageInfos", reflect.TypeOf([]ImageInfo{})},
		{"LockedFields", reflect.TypeOf([]MetadataField{})},
		{"Values", reflect.TypeOf(ItemValues{})},
		{"OpenAccess", reflect.TypeOf(false)},
		{"People", reflect.TypeOf([]PersonCredit{})},
		{"TrailerTypes", reflect.TypeOf([]TrailerType{})},
		{"LinkedChildren", reflect.TypeOf([]LinkedChildRef{})},
		{"AdditionalParts", reflect.TypeOf([]string{})},
		{"Extra", reflect.TypeOf(ExtraFields{})},
	})

	for _, legacy := range []string{
		"AlbumArtist",
		"AlbumArtists",
		"StoredSeriesPresentationUniqueKey",
		"Number",
		"PlaylistMediaType",
		"ChannelType",
	} {
		if _, found := reflect.TypeOf(Item{}).FieldByName(legacy); found {
			t.Errorf("Item still exposes legacy duplicate field %q", legacy)
		}
	}
}

// Sources: docs/02-storage.md:75-84,93-102; BaseItemMapper.cs:217-354; and
// LinkedChildEntity.cs:27-30. These normalized satellites must stay typed so
// query/persistence code cannot reintroduce duplicate scalar aliases.
func TestItemNormalizedSatellitesHaveCanonicalShapes(t *testing.T) {
	assertExactFieldTypes(t, reflect.TypeOf(ItemValues{}), []fieldType{
		{"Genres", reflect.TypeOf([]string{})},
		{"Tags", reflect.TypeOf([]string{})},
		{"InheritedTags", reflect.TypeOf([]string{})},
		{"Artists", reflect.TypeOf([]string{})},
		{"AlbumArtists", reflect.TypeOf([]string{})},
		{"Studios", reflect.TypeOf([]string{})},
		{"ProductionLocations", reflect.TypeOf([]string{})},
	})
	assertExactFieldTypes(t, reflect.TypeOf(ImageInfo{}), []fieldType{
		{"Type", reflect.TypeOf(ImageType(0))},
		{"Index", reflect.TypeOf(int(0))},
		{"Path", reflect.TypeOf("")},
		{"DateModified", reflect.TypeOf((*time.Time)(nil))},
		{"Width", reflect.TypeOf(int(0))},
		{"Height", reflect.TypeOf(int(0))},
		{"BlurHash", reflect.TypeOf((*string)(nil))},
	})
	assertExactFieldTypes(t, reflect.TypeOf(PersonCredit{}), []fieldType{
		{"PersonID", reflect.TypeOf(dto.UUID{})},
		{"Name", reflect.TypeOf("")},
		{"Role", reflect.TypeOf((*string)(nil))},
		{"Type", reflect.TypeOf(PersonKind(0))},
		{"SortOrder", reflect.TypeOf((*int)(nil))},
		{"ImageURL", reflect.TypeOf("")},
		{"ProviderIDs", reflect.TypeOf(ProviderIDs{})},
	})
	assertExactFieldTypes(t, reflect.TypeOf(LinkedChildRef{}), []fieldType{
		{"ItemID", reflect.TypeOf(dto.UUID{})},
		{"Type", reflect.TypeOf(LinkedChildType(0))},
		{"SortOrder", reflect.TypeOf((*int)(nil))},
	})
	for _, enum := range []reflect.Type{
		reflect.TypeOf(ImageType(0)),
		reflect.TypeOf(MetadataField(0)),
		reflect.TypeOf(TrailerType(0)),
		reflect.TypeOf(LinkedChildType(0)),
		reflect.TypeOf(UnratedItem(0)),
		reflect.TypeOf(ExtraType(0)),
		reflect.TypeOf(VideoType(0)),
		reflect.TypeOf(IsoType(0)),
		reflect.TypeOf(Video3DFormat(0)),
		reflect.TypeOf(SeriesStatus(0)),
		reflect.TypeOf(ProgramAudio(0)),
	} {
		if enum.Kind() != reflect.Uint8 {
			t.Errorf("%s must be a uint8-backed typed enum, got kind %s", enum, enum.Kind())
		}
	}
}

// Image dimensions are required scalar metadata in Jellyfin's image model.
// They must remain JSON numbers rather than nullable pointers so every decoded
// image has a concrete dimension value at the public model seam.
func TestImageInfoDimensionsRoundTripAsNumbers(t *testing.T) {
	var image ImageInfo
	if err := json.Unmarshal([]byte(`{"width":1920,"height":1080}`), &image); err != nil {
		t.Fatalf("json.Unmarshal(ImageInfo dimensions): %v", err)
	}

	encoded, err := json.Marshal(image)
	if err != nil {
		t.Fatalf("json.Marshal(ImageInfo dimensions): %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal(encoded ImageInfo): %v", err)
	}
	for field, want := range map[string]string{"width": "1920", "height": "1080"} {
		if got := string(fields[field]); got != want {
			t.Errorf("ImageInfo JSON %s = %s, want numeric %s", field, got, want)
		}
	}
}

// A missing person-credit role and an explicitly empty role have distinct
// relation semantics and must retain that distinction through JSON storage.
func TestPersonCreditRoleRoundTripDistinguishesNilAndEmpty(t *testing.T) {
	cases := []struct {
		name              string
		input             string
		roleMustBePresent bool
	}{
		{name: "nil role", input: `{"name":"Ada"}`, roleMustBePresent: false},
		{name: "explicit empty role", input: `{"name":"Ada","role":""}`, roleMustBePresent: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var credit PersonCredit
			if err := json.Unmarshal([]byte(tc.input), &credit); err != nil {
				t.Fatalf("json.Unmarshal(PersonCredit): %v", err)
			}
			encoded, err := json.Marshal(credit)
			if err != nil {
				t.Fatalf("json.Marshal(PersonCredit): %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("json.Unmarshal(encoded PersonCredit): %v", err)
			}
			role, found := fields["role"]
			if found != tc.roleMustBePresent {
				t.Fatalf("PersonCredit role present after JSON round-trip = %t, want %t; JSON = %s", found, tc.roleMustBePresent, encoded)
			}
			if tc.roleMustBePresent && string(role) != `""` {
				t.Fatalf("explicit empty PersonCredit role after JSON round-trip = %s, want empty string", role)
			}
		})
	}
}

// Source: upstream/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// Jellyfin.Data/Enums/PersonKind.cs. Person credit type ordinals persist with
// the relation, so their declaration order is part of the compatibility seam.
func TestPersonKindPinnedOrdinals(t *testing.T) {
	cases := []struct {
		name string
		kind PersonKind
	}{
		{"Unknown", PersonKindUnknown},
		{"Actor", PersonKindActor},
		{"Director", PersonKindDirector},
		{"Composer", PersonKindComposer},
		{"Writer", PersonKindWriter},
		{"GuestStar", PersonKindGuestStar},
		{"Producer", PersonKindProducer},
		{"Conductor", PersonKindConductor},
		{"Lyricist", PersonKindLyricist},
		{"Arranger", PersonKindArranger},
		{"Engineer", PersonKindEngineer},
		{"Mixer", PersonKindMixer},
		{"Remixer", PersonKindRemixer},
		{"Creator", PersonKindCreator},
		{"Artist", PersonKindArtist},
		{"AlbumArtist", PersonKindAlbumArtist},
		{"Author", PersonKindAuthor},
		{"Illustrator", PersonKindIllustrator},
		{"Penciller", PersonKindPenciller},
		{"Inker", PersonKindInker},
		{"Colorist", PersonKindColorist},
		{"Letterer", PersonKindLetterer},
		{"CoverArtist", PersonKindCoverArtist},
		{"Editor", PersonKindEditor},
		{"Translator", PersonKindTranslator},
		{"Narrator", PersonKindNarrator},
	}
	if got := reflect.TypeOf(PersonKind(0)).Kind(); got != reflect.Uint8 {
		t.Fatalf("PersonKind backing kind = %s, want uint8", got)
	}
	for ordinal, tc := range cases {
		if got := uint8(tc.kind); got != uint8(ordinal) {
			t.Errorf("PersonKind%s = %d, want %d", tc.name, got, ordinal)
		}
		if got := tc.kind.String(); got != tc.name {
			t.Errorf("PersonKind(%d).String() = %q, want %q", tc.kind, got, tc.name)
		}
	}
	for _, invalid := range []PersonKind{PersonKind(26), PersonKind(255)} {
		if got := invalid.String(); got != "" {
			t.Errorf("invalid PersonKind(%d).String() = %q, want empty", invalid, got)
		}
	}
}

// Source: upstream/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// MediaBrowser.Controller/Entities/LinkedChildType.cs:6-23. Linked-child type
// ordinals persist with each normalized relationship; the complete String map
// must therefore remain exactly compatible with the pinned upstream enum.
func TestLinkedChildTypePinnedNamesOrdinalsAndExhaustiveness(t *testing.T) {
	cases := []struct {
		name  string
		value LinkedChildType
		want  uint8
	}{
		{name: "Manual", value: LinkedChildTypeManual, want: 0},
		{name: "Shortcut", value: LinkedChildTypeShortcut, want: 1},
		{name: "LocalAlternateVersion", value: LinkedChildTypeLocalAlternateVersion, want: 2},
		{name: "LinkedAlternateVersion", value: LinkedChildTypeLinkedAlternateVersion, want: 3},
	}
	if len(cases) != 4 {
		t.Fatalf("LinkedChildType table has %d values, want 4", len(cases))
	}
	assertPinnedEnum(t, "LinkedChildType", reflect.TypeOf(LinkedChildType(0)), cases, func(value LinkedChildType) string { return value.String() })
	if got := LinkedChildType(4).String(); got != "" {
		t.Errorf("invalid LinkedChildType(4).String() = %q, want empty", got)
	}
}

// Sources: docs/02-storage.md:79-80 and upstream/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// MediaBrowser.Model/Entities/MediaUrl.cs:5-8; MediaBrowser.Controller/Entities/
// Folder.cs:59-60; CollectionFolder.cs:65-79; Movies/BoxSet.cs:45-46;
// Movies/Movie.cs:38-45; TV/Series.cs:29-69; Photo.cs:40-62; IHasAspectRatio.cs:8-15;
// and UserView.cs:43-54. Extra is intentionally limited to non-query, per-kind
// payloads; queryable facts and normalized relationships stay outside it.
func TestExtraFieldsExactlyRepresentNonQuerySubtypePayloads(t *testing.T) {
	assertExactFieldContracts(t, reflect.TypeOf(ExtraFields{}), []fieldContract{
		{"RemoteTrailers", reflect.TypeOf([]MediaURL{}), `json:"remoteTrailers,omitempty"`},
		{"LiveTV", reflect.TypeOf((*LiveTVExtra)(nil)), `json:"liveTV,omitempty"`},
		{"Episode", reflect.TypeOf((*EpisodeExtra)(nil)), `json:"episode,omitempty"`},
		{"Folder", reflect.TypeOf((*FolderExtra)(nil)), `json:"folder,omitempty"`},
		{"CollectionFolder", reflect.TypeOf((*CollectionFolderExtra)(nil)), `json:"collectionFolder,omitempty"`},
		{"BoxSet", reflect.TypeOf((*BoxSetExtra)(nil)), `json:"boxSet,omitempty"`},
		{"Movie", reflect.TypeOf((*MovieExtra)(nil)), `json:"movie,omitempty"`},
		{"Series", reflect.TypeOf((*SeriesExtra)(nil)), `json:"series,omitempty"`},
		{"Photo", reflect.TypeOf((*PhotoExtra)(nil)), `json:"photo,omitempty"`},
		{"Video", reflect.TypeOf((*VideoExtra)(nil)), `json:"video,omitempty"`},
		{"UserView", reflect.TypeOf((*UserViewExtra)(nil)), `json:"userView,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(MediaURL{}), []fieldContract{
		{"URL", reflect.TypeOf((*string)(nil)), `json:"url,omitempty"`},
		{"Name", reflect.TypeOf((*string)(nil)), `json:"name,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(LiveTVExtra{}), []fieldContract{
		{"Number", reflect.TypeOf((*string)(nil)), `json:"number,omitempty"`},
		{"ChannelType", reflect.TypeOf(ChannelType(0)), `json:"channelType,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(EpisodeExtra{}), []fieldContract{
		{"IndexNumberEnd", reflect.TypeOf((*int)(nil)), `json:"indexNumberEnd,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(FolderExtra{}), []fieldContract{
		{"IsRoot", reflect.TypeOf(false), `json:"isRoot,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(CollectionFolderExtra{}), []fieldContract{
		{"CollectionType", reflect.TypeOf((*CollectionType)(nil)), `json:"collectionType,omitempty"`},
		{"PhysicalFolderIDs", reflect.TypeOf([]dto.UUID{}), `json:"physicalFolderIds,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(BoxSetExtra{}), []fieldContract{
		{"DisplayOrder", reflect.TypeOf((*string)(nil)), `json:"displayOrder,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(MovieExtra{}), []fieldContract{
		{"TmdbCollectionName", reflect.TypeOf((*string)(nil)), `json:"tmdbCollectionName,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(SeriesExtra{}), []fieldContract{
		{"AirDays", reflect.TypeOf([]DayOfWeek{}), `json:"airDays,omitempty"`},
		{"AirTime", reflect.TypeOf((*string)(nil)), `json:"airTime,omitempty"`},
		{"DisplayOrder", reflect.TypeOf((*string)(nil)), `json:"displayOrder,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(PhotoExtra{}), []fieldContract{
		{"CameraMake", reflect.TypeOf((*string)(nil)), `json:"cameraMake,omitempty"`},
		{"CameraModel", reflect.TypeOf((*string)(nil)), `json:"cameraModel,omitempty"`},
		{"Software", reflect.TypeOf((*string)(nil)), `json:"software,omitempty"`},
		{"ExposureTime", reflect.TypeOf((*float64)(nil)), `json:"exposureTime,omitempty"`},
		{"FocalLength", reflect.TypeOf((*float64)(nil)), `json:"focalLength,omitempty"`},
		{"Aperture", reflect.TypeOf((*float64)(nil)), `json:"aperture,omitempty"`},
		{"ShutterSpeed", reflect.TypeOf((*float64)(nil)), `json:"shutterSpeed,omitempty"`},
		{"Latitude", reflect.TypeOf((*float64)(nil)), `json:"latitude,omitempty"`},
		{"Longitude", reflect.TypeOf((*float64)(nil)), `json:"longitude,omitempty"`},
		{"Altitude", reflect.TypeOf((*float64)(nil)), `json:"altitude,omitempty"`},
		{"Orientation", reflect.TypeOf((*ImageOrientation)(nil)), `json:"orientation,omitempty"`},
		{"IsoSpeedRating", reflect.TypeOf((*int)(nil)), `json:"isoSpeedRating,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(VideoExtra{}), []fieldContract{
		{"AspectRatio", reflect.TypeOf((*string)(nil)), `json:"aspectRatio,omitempty"`},
	})
	assertExactFieldContracts(t, reflect.TypeOf(UserViewExtra{}), []fieldContract{
		{"ViewType", reflect.TypeOf((*CollectionType)(nil)), `json:"viewType,omitempty"`},
		{"DisplayParentID", reflect.TypeOf(dto.UUID{}), `json:"displayParentId,omitempty"`},
		{"UserID", reflect.TypeOf((*dto.UUID)(nil)), `json:"userId,omitempty"`},
	})

	assertFieldsAbsent(t, reflect.TypeOf(ExtraFields{}), []string{
		"Playlist", "PlaylistExtra", "PlaylistMediaType", "TrailerTypes", "Shares", "SharedItems",
		"FileSystemChildren", "MediaSources", "MediaStreams", "Chapters",
	})
	assertFieldsAbsent(t, reflect.TypeOf(SeriesExtra{}), []string{"Status"})
	assertFieldsAbsent(t, reflect.TypeOf(VideoExtra{}), []string{
		"MediaType", "VideoType", "IsoType", "Video3DFormat", "IsMovie", "IsSeries", "IsRepeat",
		"MediaSources", "MediaStreams",
	})
	assertFieldsAbsent(t, reflect.TypeOf(BoxSetExtra{}), []string{"LibraryFolderIDs", "Shares", "SharedItems"})
}

// Every pointer below carries a nullable database fact. A populated payload must
// survive JSON storage without losing subtype ownership or conflating values.
func TestExtraFieldsRoundTripPopulatedSubtypePayloads(t *testing.T) {
	collectionType := CollectionTypeTVShows
	viewType := CollectionTypeMovies
	displayOrder := "airdate"
	tmdbCollection := "The Collection"
	airTime := "20:30"
	cameraMake := "Camera Co"
	cameraModel := "Model 7"
	software := "Firmware 1"
	aspectRatio := "16:9"
	exposureTime := 0.008
	focalLength := 35.0
	aperture := 2.8
	shutterSpeed := 125.0
	latitude := 48.8566
	longitude := 2.3522
	altitude := 35.5
	orientation := ImageOrientationRightBottom
	isoSpeed := 400
	indexEnd := 9
	userID := dto.UUID{0x10}
	displayParentID := dto.UUID{0x20}
	want := ExtraFields{
		RemoteTrailers: nil,
		LiveTV:         nil,
		Episode:        &EpisodeExtra{IndexNumberEnd: &indexEnd},
		Folder:         &FolderExtra{IsRoot: true},
		CollectionFolder: &CollectionFolderExtra{
			CollectionType:    &collectionType,
			PhysicalFolderIDs: []dto.UUID{{0x30}, {0x31}},
		},
		BoxSet: &BoxSetExtra{DisplayOrder: &displayOrder},
		Movie:  &MovieExtra{TmdbCollectionName: &tmdbCollection},
		Series: &SeriesExtra{
			AirDays:      []DayOfWeek{DayOfWeekSunday, DayOfWeekWednesday},
			AirTime:      &airTime,
			DisplayOrder: &displayOrder,
		},
		Photo: &PhotoExtra{
			CameraMake:     &cameraMake,
			CameraModel:    &cameraModel,
			Software:       &software,
			ExposureTime:   &exposureTime,
			FocalLength:    &focalLength,
			Aperture:       &aperture,
			ShutterSpeed:   &shutterSpeed,
			Latitude:       &latitude,
			Longitude:      &longitude,
			Altitude:       &altitude,
			Orientation:    &orientation,
			IsoSpeedRating: &isoSpeed,
		},
		Video: &VideoExtra{AspectRatio: &aspectRatio},
		UserView: &UserViewExtra{
			ViewType:        &viewType,
			DisplayParentID: displayParentID,
			UserID:          &userID,
		},
	}
	if err := json.Unmarshal([]byte(`{"remoteTrailers":[{"url":"https://trailers.example/one","name":"Teaser"}],"liveTV":{"number":"7.1","channelType":1}}`), &want); err != nil {
		t.Fatalf("json.Unmarshal(populated MediaURL and LiveTV payload): %v", err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(ExtraFields): %v", err)
	}
	var got ExtraFields
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("json.Unmarshal(ExtraFields): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtraFields JSON round-trip = %#v, want %#v", got, want)
	}
}

// Omitting a nullable text fact is distinct from explicitly persisting it as an
// empty string. Box-set display order is a compact regression boundary for all
// pointer-valued subtype text fields.
func TestExtraFieldsNestedPointerRoundTripDistinguishesNilAndEmpty(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		fieldExists bool
	}{
		{name: "nil display order", input: `{"boxSet":{}}`, fieldExists: false},
		{name: "explicit empty display order", input: `{"boxSet":{"displayOrder":""}}`, fieldExists: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value ExtraFields
			if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
				t.Fatalf("json.Unmarshal(ExtraFields): %v", err)
			}
			if value.BoxSet == nil {
				t.Fatal("decoded boxSet is nil")
			}
			if (value.BoxSet.DisplayOrder != nil) != tc.fieldExists {
				t.Fatalf("decoded DisplayOrder present = %t, want %t", value.BoxSet.DisplayOrder != nil, tc.fieldExists)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("json.Marshal(ExtraFields): %v", err)
			}
			var object map[string]map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("json.Unmarshal(encoded ExtraFields): %v", err)
			}
			got, found := object["boxSet"]["displayOrder"]
			if found != tc.fieldExists {
				t.Fatalf("displayOrder presence after JSON round-trip = %t, want %t; JSON = %s", found, tc.fieldExists, encoded)
			}
			if tc.fieldExists && string(got) != `""` {
				t.Fatalf("explicit empty DisplayOrder after JSON round-trip = %s, want empty string", got)
			}
		})
	}
}

// MediaUrl.Url, MediaUrl.Name, and LiveTvChannel.Number are nullable strings
// under the pinned upstream model. JSON storage must preserve the difference
// between an omitted null property and a present empty property.
func TestExtraFieldsNullableTextRoundTripDistinguishesNilAndEmpty(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		payload     string
		field       string
		fieldExists bool
	}{
		{
			name:        "MediaURL URL nil omits",
			input:       `{"remoteTrailers":[{}]}`,
			payload:     "remoteTrailers",
			field:       "url",
			fieldExists: false,
		},
		{
			name:        "MediaURL URL explicit empty emits",
			input:       `{"remoteTrailers":[{"url":""}]}`,
			payload:     "remoteTrailers",
			field:       "url",
			fieldExists: true,
		},
		{
			name:        "MediaURL Name nil omits",
			input:       `{"remoteTrailers":[{}]}`,
			payload:     "remoteTrailers",
			field:       "name",
			fieldExists: false,
		},
		{
			name:        "MediaURL Name explicit empty emits",
			input:       `{"remoteTrailers":[{"name":""}]}`,
			payload:     "remoteTrailers",
			field:       "name",
			fieldExists: true,
		},
		{
			name:        "LiveTV Number nil omits",
			input:       `{"liveTV":{}}`,
			payload:     "liveTV",
			field:       "number",
			fieldExists: false,
		},
		{
			name:        "LiveTV Number explicit empty emits",
			input:       `{"liveTV":{"number":""}}`,
			payload:     "liveTV",
			field:       "number",
			fieldExists: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value ExtraFields
			if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
				t.Fatalf("json.Unmarshal(ExtraFields): %v", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("json.Marshal(ExtraFields): %v", err)
			}

			var object map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &object); err != nil {
				t.Fatalf("json.Unmarshal(encoded ExtraFields): %v", err)
			}
			var payload map[string]json.RawMessage
			if tc.payload == "remoteTrailers" {
				var trailers []map[string]json.RawMessage
				if err := json.Unmarshal(object[tc.payload], &trailers); err != nil {
					t.Fatalf("json.Unmarshal(encoded remoteTrailers): %v", err)
				}
				if len(trailers) != 1 {
					t.Fatalf("encoded remoteTrailers length = %d, want 1; JSON = %s", len(trailers), encoded)
				}
				payload = trailers[0]
			} else if err := json.Unmarshal(object[tc.payload], &payload); err != nil {
				t.Fatalf("json.Unmarshal(encoded liveTV): %v", err)
			}

			got, found := payload[tc.field]
			if found != tc.fieldExists {
				t.Fatalf("%s.%s presence after JSON round-trip = %t, want %t; JSON = %s", tc.payload, tc.field, found, tc.fieldExists, encoded)
			}
			if tc.fieldExists && string(got) != `""` {
				t.Fatalf("%s.%s after JSON round-trip = %s, want empty string", tc.payload, tc.field, got)
			}
		})
	}
}

// Sources: upstream/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// Jellyfin.Data/Enums/CollectionType.cs:9-164; System.DayOfWeek; and
// MediaBrowser.Model/Drawing/ImageOrientation.cs:5-15. These ordinals are
// serialized in Extra and must remain stable.
func TestExtraPayloadEnumsHavePinnedNamesOrdinalsAndInvalidString(t *testing.T) {
	collectionTypes := []struct {
		name  string
		value CollectionType
		want  uint8
	}{
		{"unknown", CollectionTypeUnknown, 0},
		{"movies", CollectionTypeMovies, 1},
		{"tvshows", CollectionTypeTVShows, 2},
		{"music", CollectionTypeMusic, 3},
		{"musicvideos", CollectionTypeMusicVideos, 4},
		{"trailers", CollectionTypeTrailers, 5},
		{"homevideos", CollectionTypeHomeVideos, 6},
		{"boxsets", CollectionTypeBoxSets, 7},
		{"books", CollectionTypeBooks, 8},
		{"photos", CollectionTypePhotos, 9},
		{"livetv", CollectionTypeLiveTV, 10},
		{"playlists", CollectionTypePlaylists, 11},
		{"folders", CollectionTypeFolders, 12},
		{"tvshowseries", CollectionTypeTVShowSeries, 101},
		{"tvgenres", CollectionTypeTVGenres, 102},
		{"tvgenre", CollectionTypeTVGenre, 103},
		{"tvlatest", CollectionTypeTVLatest, 104},
		{"tvnextup", CollectionTypeTVNextUp, 105},
		{"tvresume", CollectionTypeTVResume, 106},
		{"tvfavoriteseries", CollectionTypeTVFavoriteSeries, 107},
		{"tvfavoriteepisodes", CollectionTypeTVFavoriteEpisodes, 108},
		{"movielatest", CollectionTypeMovieLatest, 109},
		{"movieresume", CollectionTypeMovieResume, 110},
		{"moviemovies", CollectionTypeMovieMovies, 111},
		{"moviecollection", CollectionTypeMovieCollection, 112},
		{"moviefavorites", CollectionTypeMovieFavorites, 113},
		{"moviegenres", CollectionTypeMovieGenres, 114},
		{"moviegenre", CollectionTypeMovieGenre, 115},
	}
	assertPinnedEnum(t, "CollectionType", reflect.TypeOf(CollectionType(0)), collectionTypes, func(value CollectionType) string { return value.String() })
	for _, invalid := range []CollectionType{CollectionType(13), CollectionType(255)} {
		if got := invalid.String(); got != "" {
			t.Errorf("invalid CollectionType(%d).String() = %q, want empty", invalid, got)
		}
	}

	days := []struct {
		name  string
		value DayOfWeek
		want  uint8
	}{
		{"Sunday", DayOfWeekSunday, 0},
		{"Monday", DayOfWeekMonday, 1},
		{"Tuesday", DayOfWeekTuesday, 2},
		{"Wednesday", DayOfWeekWednesday, 3},
		{"Thursday", DayOfWeekThursday, 4},
		{"Friday", DayOfWeekFriday, 5},
		{"Saturday", DayOfWeekSaturday, 6},
	}
	assertPinnedEnum(t, "DayOfWeek", reflect.TypeOf(DayOfWeek(0)), days, func(value DayOfWeek) string { return value.String() })
	if got := DayOfWeek(7).String(); got != "" {
		t.Errorf("invalid DayOfWeek(7).String() = %q, want empty", got)
	}

	orientations := []struct {
		name  string
		value ImageOrientation
		want  uint8
	}{
		{"TopLeft", ImageOrientationTopLeft, 1},
		{"TopRight", ImageOrientationTopRight, 2},
		{"BottomRight", ImageOrientationBottomRight, 3},
		{"BottomLeft", ImageOrientationBottomLeft, 4},
		{"LeftTop", ImageOrientationLeftTop, 5},
		{"RightTop", ImageOrientationRightTop, 6},
		{"RightBottom", ImageOrientationRightBottom, 7},
		{"LeftBottom", ImageOrientationLeftBottom, 8},
	}
	assertPinnedEnum(t, "ImageOrientation", reflect.TypeOf(ImageOrientation(0)), orientations, func(value ImageOrientation) string { return value.String() })
	for _, invalid := range []ImageOrientation{ImageOrientation(0), ImageOrientation(9), ImageOrientation(255)} {
		if got := invalid.String(); got != "" {
			t.Errorf("invalid ImageOrientation(%d).String() = %q, want empty", invalid, got)
		}
	}
}

func assertPinnedEnum[T ~uint8](t *testing.T, enumName string, enumType reflect.Type, cases []struct {
	name  string
	value T
	want  uint8
}, stringOf func(T) string,
) {
	t.Helper()
	if got := enumType.Kind(); got != reflect.Uint8 {
		t.Fatalf("%s backing kind = %s, want uint8", enumName, got)
	}
	for _, tc := range cases {
		if got := uint8(tc.value); got != tc.want {
			t.Errorf("%s%s = %d, want %d", enumName, tc.name, got, tc.want)
		}
		if got := stringOf(tc.value); got != tc.name {
			t.Errorf("%s(%d).String() = %q, want %q", enumName, tc.value, got, tc.name)
		}
	}
}

type fieldType struct {
	name string
	typ  reflect.Type
}

type fieldContract struct {
	name string
	typ  reflect.Type
	tag  string
}

func assertExactFieldContracts(t *testing.T, typ reflect.Type, want []fieldContract) {
	t.Helper()
	if typ.NumField() != len(want) {
		t.Errorf("%s has %d fields, want exactly %d", typ, typ.NumField(), len(want))
	}
	for _, expected := range want {
		field, found := typ.FieldByName(expected.name)
		if !found {
			t.Errorf("%s is missing field %s", typ, expected.name)
			continue
		}
		if field.Type != expected.typ {
			t.Errorf("%s.%s has type %s, want %s", typ, expected.name, field.Type, expected.typ)
		}
		if got := string(field.Tag); got != expected.tag {
			t.Errorf("%s.%s tag = %q, want %q", typ, expected.name, got, expected.tag)
		}
	}
}

func assertFieldsAbsent(t *testing.T, typ reflect.Type, names []string) {
	t.Helper()
	for _, name := range names {
		if _, found := typ.FieldByName(name); found {
			t.Errorf("%s must not expose duplicate field %q", typ, name)
		}
	}
}

func assertExactFieldTypes(t *testing.T, typ reflect.Type, want []fieldType) {
	t.Helper()
	if typ.NumField() != len(want) {
		t.Errorf("%s has %d fields, want exactly %d", typ, typ.NumField(), len(want))
	}
	for _, expected := range want {
		field, found := typ.FieldByName(expected.name)
		if !found {
			t.Errorf("%s is missing field %s", typ, expected.name)
			continue
		}
		if field.Type != expected.typ {
			t.Errorf("%s.%s has type %s, want %s", typ, expected.name, field.Type, expected.typ)
		}
	}
}
