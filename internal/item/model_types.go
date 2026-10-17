// SPDX-License-Identifier: GPL-2.0-only

package item

import (
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
)

// UnratedItem matches Jellyfin.Data.Enums.UnratedItem.
type UnratedItem uint8

const (
	UnratedItemMovie UnratedItem = iota
	UnratedItemTrailer
	UnratedItemSeries
	UnratedItemMusic
	UnratedItemBook
	UnratedItemLiveTvChannel
	UnratedItemLiveTvProgram
	UnratedItemChannelContent
	UnratedItemOther
)

func (u UnratedItem) String() string {
	switch u {
	case UnratedItemMovie:
		return "Movie"
	case UnratedItemTrailer:
		return "Trailer"
	case UnratedItemSeries:
		return "Series"
	case UnratedItemMusic:
		return "Music"
	case UnratedItemBook:
		return "Book"
	case UnratedItemLiveTvChannel:
		return "LiveTvChannel"
	case UnratedItemLiveTvProgram:
		return "LiveTvProgram"
	case UnratedItemChannelContent:
		return "ChannelContent"
	case UnratedItemOther:
		return "Other"
	default:
		return ""
	}
}

// ExtraType matches MediaBrowser.Model.Entities.ExtraType.
type ExtraType uint8

const (
	ExtraTypeUnknown ExtraType = iota
	ExtraTypeClip
	ExtraTypeTrailer
	ExtraTypeBehindTheScenes
	ExtraTypeDeletedScene
	ExtraTypeInterview
	ExtraTypeScene
	ExtraTypeSample
	ExtraTypeThemeSong
	ExtraTypeThemeVideo
	ExtraTypeFeaturette
	ExtraTypeShort
)

func (e ExtraType) String() string {
	switch e {
	case ExtraTypeUnknown:
		return "Unknown"
	case ExtraTypeClip:
		return "Clip"
	case ExtraTypeTrailer:
		return "Trailer"
	case ExtraTypeBehindTheScenes:
		return "BehindTheScenes"
	case ExtraTypeDeletedScene:
		return "DeletedScene"
	case ExtraTypeInterview:
		return "Interview"
	case ExtraTypeScene:
		return "Scene"
	case ExtraTypeSample:
		return "Sample"
	case ExtraTypeThemeSong:
		return "ThemeSong"
	case ExtraTypeThemeVideo:
		return "ThemeVideo"
	case ExtraTypeFeaturette:
		return "Featurette"
	case ExtraTypeShort:
		return "Short"
	default:
		return ""
	}
}

// VideoType matches MediaBrowser.Model.Entities.VideoType.
type VideoType uint8

const (
	VideoTypeVideoFile VideoType = iota
	VideoTypeIso
	VideoTypeDvd
	VideoTypeBluRay
)

func (v VideoType) String() string {
	switch v {
	case VideoTypeVideoFile:
		return "VideoFile"
	case VideoTypeIso:
		return "Iso"
	case VideoTypeDvd:
		return "Dvd"
	case VideoTypeBluRay:
		return "BluRay"
	default:
		return ""
	}
}

// IsoType matches MediaBrowser.Model.Entities.IsoType.
type IsoType uint8

const (
	IsoTypeDvd IsoType = iota
	IsoTypeBluRay
)

func (i IsoType) String() string {
	switch i {
	case IsoTypeDvd:
		return "Dvd"
	case IsoTypeBluRay:
		return "BluRay"
	default:
		return ""
	}
}

// Video3DFormat matches MediaBrowser.Model.Entities.Video3DFormat.
type Video3DFormat uint8

const (
	Video3DFormatHalfSideBySide Video3DFormat = iota
	Video3DFormatFullSideBySide
	Video3DFormatFullTopAndBottom
	Video3DFormatHalfTopAndBottom
	Video3DFormatMVC
)

func (v Video3DFormat) String() string {
	switch v {
	case Video3DFormatHalfSideBySide:
		return "HalfSideBySide"
	case Video3DFormatFullSideBySide:
		return "FullSideBySide"
	case Video3DFormatFullTopAndBottom:
		return "FullTopAndBottom"
	case Video3DFormatHalfTopAndBottom:
		return "HalfTopAndBottom"
	case Video3DFormatMVC:
		return "MVC"
	default:
		return ""
	}
}

// ImageType matches MediaBrowser.Model.Entities.ImageType.
type ImageType uint8

const (
	ImageTypePrimary ImageType = iota
	ImageTypeArt
	ImageTypeBackdrop
	ImageTypeBanner
	ImageTypeLogo
	ImageTypeThumb
	ImageTypeDisc
	ImageTypeBox
	ImageTypeScreenshot
	ImageTypeMenu
	ImageTypeChapter
	ImageTypeBoxRear
	ImageTypeProfile
)

func (i ImageType) String() string {
	switch i {
	case ImageTypePrimary:
		return "Primary"
	case ImageTypeArt:
		return "Art"
	case ImageTypeBackdrop:
		return "Backdrop"
	case ImageTypeBanner:
		return "Banner"
	case ImageTypeLogo:
		return "Logo"
	case ImageTypeThumb:
		return "Thumb"
	case ImageTypeDisc:
		return "Disc"
	case ImageTypeBox:
		return "Box"
	case ImageTypeScreenshot:
		return "Screenshot"
	case ImageTypeMenu:
		return "Menu"
	case ImageTypeChapter:
		return "Chapter"
	case ImageTypeBoxRear:
		return "BoxRear"
	case ImageTypeProfile:
		return "Profile"
	default:
		return ""
	}
}

// MetadataField matches MediaBrowser.Model.Entities.MetadataField.
type MetadataField uint8

const (
	MetadataFieldCast MetadataField = iota
	MetadataFieldGenres
	MetadataFieldProductionLocations
	MetadataFieldStudios
	MetadataFieldTags
	MetadataFieldName
	MetadataFieldOverview
	MetadataFieldRuntime
	MetadataFieldOfficialRating
)

func (m MetadataField) String() string {
	switch m {
	case MetadataFieldCast:
		return "Cast"
	case MetadataFieldGenres:
		return "Genres"
	case MetadataFieldProductionLocations:
		return "ProductionLocations"
	case MetadataFieldStudios:
		return "Studios"
	case MetadataFieldTags:
		return "Tags"
	case MetadataFieldName:
		return "Name"
	case MetadataFieldOverview:
		return "Overview"
	case MetadataFieldRuntime:
		return "Runtime"
	case MetadataFieldOfficialRating:
		return "OfficialRating"
	default:
		return ""
	}
}

// TrailerType matches MediaBrowser.Model.Entities.TrailerType.
type TrailerType uint8

const (
	TrailerTypeComingSoonToTheaters TrailerType = 1 + iota
	TrailerTypeComingSoonToDvd
	TrailerTypeComingSoonToStreaming
	TrailerTypeArchive
	TrailerTypeLocalTrailer
)

func (t TrailerType) String() string {
	switch t {
	case TrailerTypeComingSoonToTheaters:
		return "ComingSoonToTheaters"
	case TrailerTypeComingSoonToDvd:
		return "ComingSoonToDvd"
	case TrailerTypeComingSoonToStreaming:
		return "ComingSoonToStreaming"
	case TrailerTypeArchive:
		return "Archive"
	case TrailerTypeLocalTrailer:
		return "LocalTrailer"
	default:
		return ""
	}
}

// LinkedChildType matches MediaBrowser.Controller.Entities.LinkedChildType.
type LinkedChildType uint8

const (
	LinkedChildTypeManual LinkedChildType = iota
	LinkedChildTypeShortcut
	LinkedChildTypeLocalAlternateVersion
	LinkedChildTypeLinkedAlternateVersion
)

func (l LinkedChildType) String() string {
	switch l {
	case LinkedChildTypeManual:
		return "Manual"
	case LinkedChildTypeShortcut:
		return "Shortcut"
	case LinkedChildTypeLocalAlternateVersion:
		return "LocalAlternateVersion"
	case LinkedChildTypeLinkedAlternateVersion:
		return "LinkedAlternateVersion"
	default:
		return ""
	}
}

// SeriesStatus matches MediaBrowser.Model.Entities.SeriesStatus.
type SeriesStatus uint8

const (
	SeriesStatusContinuing SeriesStatus = iota
	SeriesStatusEnded
	SeriesStatusUnreleased
)

func (s SeriesStatus) String() string {
	switch s {
	case SeriesStatusContinuing:
		return "Continuing"
	case SeriesStatusEnded:
		return "Ended"
	case SeriesStatusUnreleased:
		return "Unreleased"
	default:
		return ""
	}
}

// ProgramAudio matches MediaBrowser.Model.LiveTv.ProgramAudio.
type ProgramAudio uint8

const (
	ProgramAudioMono ProgramAudio = iota
	ProgramAudioStereo
	ProgramAudioDolby
	ProgramAudioDolbyDigital
	ProgramAudioThx
	ProgramAudioAtmos
)

func (p ProgramAudio) String() string {
	switch p {
	case ProgramAudioMono:
		return "Mono"
	case ProgramAudioStereo:
		return "Stereo"
	case ProgramAudioDolby:
		return "Dolby"
	case ProgramAudioDolbyDigital:
		return "DolbyDigital"
	case ProgramAudioThx:
		return "Thx"
	case ProgramAudioAtmos:
		return "Atmos"
	default:
		return ""
	}
}

// ItemValues is the normalized text-valued metadata satellite for an Item.
type ItemValues struct {
	Genres              []string `json:"genres,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	InheritedTags       []string `json:"inheritedTags,omitempty"`
	Artists             []string `json:"artists,omitempty"`
	AlbumArtists        []string `json:"albumArtists,omitempty"`
	Studios             []string `json:"studios,omitempty"`
	ProductionLocations []string `json:"productionLocations,omitempty"`
}

// ImageInfo is one normalized image record for an Item.
type ImageInfo struct {
	Type         ImageType  `json:"type,omitempty"`
	Index        int        `json:"index,omitempty"`
	Path         string     `json:"path,omitempty"`
	DateModified *time.Time `json:"dateModified,omitempty"`
	Width        int        `json:"width,omitempty"`
	Height       int        `json:"height,omitempty"`
	BlurHash     *string    `json:"blurHash,omitempty"`
}

// PersonKind matches Jellyfin.Data.Enums.PersonKind. Its declaration order is
// persisted with person relations.
type PersonKind uint8

const (
	PersonKindUnknown PersonKind = iota
	PersonKindActor
	PersonKindDirector
	PersonKindComposer
	PersonKindWriter
	PersonKindGuestStar
	PersonKindProducer
	PersonKindConductor
	PersonKindLyricist
	PersonKindArranger
	PersonKindEngineer
	PersonKindMixer
	PersonKindRemixer
	PersonKindCreator
	PersonKindArtist
	PersonKindAlbumArtist
	PersonKindAuthor
	PersonKindIllustrator
	PersonKindPenciller
	PersonKindInker
	PersonKindColorist
	PersonKindLetterer
	PersonKindCoverArtist
	PersonKindEditor
	PersonKindTranslator
	PersonKindNarrator
)

// String returns the canonical person kind name, or empty for an unknown value.
func (p PersonKind) String() string {
	switch p {
	case PersonKindUnknown:
		return "Unknown"
	case PersonKindActor:
		return "Actor"
	case PersonKindDirector:
		return "Director"
	case PersonKindComposer:
		return "Composer"
	case PersonKindWriter:
		return "Writer"
	case PersonKindGuestStar:
		return "GuestStar"
	case PersonKindProducer:
		return "Producer"
	case PersonKindConductor:
		return "Conductor"
	case PersonKindLyricist:
		return "Lyricist"
	case PersonKindArranger:
		return "Arranger"
	case PersonKindEngineer:
		return "Engineer"
	case PersonKindMixer:
		return "Mixer"
	case PersonKindRemixer:
		return "Remixer"
	case PersonKindCreator:
		return "Creator"
	case PersonKindArtist:
		return "Artist"
	case PersonKindAlbumArtist:
		return "AlbumArtist"
	case PersonKindAuthor:
		return "Author"
	case PersonKindIllustrator:
		return "Illustrator"
	case PersonKindPenciller:
		return "Penciller"
	case PersonKindInker:
		return "Inker"
	case PersonKindColorist:
		return "Colorist"
	case PersonKindLetterer:
		return "Letterer"
	case PersonKindCoverArtist:
		return "CoverArtist"
	case PersonKindEditor:
		return "Editor"
	case PersonKindTranslator:
		return "Translator"
	case PersonKindNarrator:
		return "Narrator"
	default:
		return ""
	}
}

// PersonCredit is one normalized person credit for an Item.
type PersonCredit struct {
	PersonID  dto.UUID   `json:"personId,omitempty"`
	Name      string     `json:"name,omitempty"`
	Role      *string    `json:"role,omitempty"`
	Type      PersonKind `json:"type,omitempty"`
	SortOrder *int       `json:"sortOrder,omitempty"`
	// ImageURL is the provider image for the person (PersonInfo.ImageUrl). The
	// scanner applies it to the Person entity as its Primary image; it is not
	// stored with the credit.
	ImageURL string `json:"imageUrl,omitempty"`
	// ProviderIDs are the provider identifiers of the person
	// (PersonInfo.ProviderIds). The scanner applies them to the Person entity;
	// they are not stored with the credit.
	ProviderIDs ProviderIDs `json:"-"`
}

// LinkedChildRef is one normalized linked child relation for an Item.
type LinkedChildRef struct {
	ItemID    dto.UUID        `json:"itemId,omitempty"`
	Type      LinkedChildType `json:"type,omitempty"`
	SortOrder *int            `json:"sortOrder,omitempty"`
}

// ExtraFields contains non-query payloads owned by their concrete item subtypes.
type ExtraFields struct {
	RemoteTrailers   []MediaURL             `json:"remoteTrailers,omitempty"`
	LiveTV           *LiveTVExtra           `json:"liveTV,omitempty"`
	Episode          *EpisodeExtra          `json:"episode,omitempty"`
	Folder           *FolderExtra           `json:"folder,omitempty"`
	CollectionFolder *CollectionFolderExtra `json:"collectionFolder,omitempty"`
	BoxSet           *BoxSetExtra           `json:"boxSet,omitempty"`
	Movie            *MovieExtra            `json:"movie,omitempty"`
	Series           *SeriesExtra           `json:"series,omitempty"`
	Photo            *PhotoExtra            `json:"photo,omitempty"`
	Video            *VideoExtra            `json:"video,omitempty"`
	UserView         *UserViewExtra         `json:"userView,omitempty"`
}

// MediaURL matches MediaBrowser.Model.Entities.MediaUrl.
type MediaURL struct {
	URL  *string `json:"url,omitempty"`
	Name *string `json:"name,omitempty"`
}

// LiveTVExtra is non-query live TV state.
type LiveTVExtra struct {
	Number      *string     `json:"number,omitempty"`
	ChannelType ChannelType `json:"channelType,omitempty"`
}

// EpisodeExtra is non-query episode state.
type EpisodeExtra struct {
	IndexNumberEnd *int `json:"indexNumberEnd,omitempty"`
}

// FolderExtra is non-query state owned by folder items.
type FolderExtra struct {
	IsRoot bool `json:"isRoot,omitempty"`
}

// CollectionFolderExtra is non-query state owned by collection folders.
// PhysicalFolderIDs records the physical Folder items that correspond to the
// configured media roots. The scanner writes it on each scan. The store
// projection reads it to flatten the root Folders from a non-recursive browse,
// matching upstream CollectionFolder.PhysicalFolderIds and the flattening in
// CollectionFolder.GetActualChildren (jellyfin/jellyfin@b04614e, :67, :349-373).
type CollectionFolderExtra struct {
	CollectionType    *CollectionType `json:"collectionType,omitempty"`
	PhysicalFolderIDs []dto.UUID      `json:"physicalFolderIds,omitempty"`
}

// BoxSetExtra is non-query state owned by box sets.
type BoxSetExtra struct {
	DisplayOrder *string `json:"displayOrder,omitempty"`
}

// MovieExtra is non-query state owned by movies.
type MovieExtra struct {
	TmdbCollectionName *string `json:"tmdbCollectionName,omitempty"`
}

// SeriesExtra is non-query state owned by series.
type SeriesExtra struct {
	AirDays      []DayOfWeek `json:"airDays,omitempty"`
	AirTime      *string     `json:"airTime,omitempty"`
	DisplayOrder *string     `json:"displayOrder,omitempty"`
}

// PhotoExtra is non-query state owned by photos.
type PhotoExtra struct {
	CameraMake     *string           `json:"cameraMake,omitempty"`
	CameraModel    *string           `json:"cameraModel,omitempty"`
	Software       *string           `json:"software,omitempty"`
	ExposureTime   *float64          `json:"exposureTime,omitempty"`
	FocalLength    *float64          `json:"focalLength,omitempty"`
	Aperture       *float64          `json:"aperture,omitempty"`
	ShutterSpeed   *float64          `json:"shutterSpeed,omitempty"`
	Latitude       *float64          `json:"latitude,omitempty"`
	Longitude      *float64          `json:"longitude,omitempty"`
	Altitude       *float64          `json:"altitude,omitempty"`
	Orientation    *ImageOrientation `json:"orientation,omitempty"`
	IsoSpeedRating *int              `json:"isoSpeedRating,omitempty"`
}

// VideoExtra is non-query state owned by videos.
type VideoExtra struct {
	AspectRatio *string `json:"aspectRatio,omitempty"`
}

// UserViewExtra is non-query state owned by user views.
type UserViewExtra struct {
	ViewType        *CollectionType `json:"viewType,omitempty"`
	DisplayParentID dto.UUID        `json:"displayParentId,omitempty"`
	UserID          *dto.UUID       `json:"userId,omitempty"`
}

// CollectionType matches Jellyfin.Data.Enums.CollectionType.
type CollectionType uint8

const (
	CollectionTypeUnknown CollectionType = iota
	CollectionTypeMovies
	CollectionTypeTVShows
	CollectionTypeMusic
	CollectionTypeMusicVideos
	CollectionTypeTrailers
	CollectionTypeHomeVideos
	CollectionTypeBoxSets
	CollectionTypeBooks
	CollectionTypePhotos
	CollectionTypeLiveTV
	CollectionTypePlaylists
	CollectionTypeFolders

	CollectionTypeTVShowSeries       CollectionType = 101
	CollectionTypeTVGenres           CollectionType = 102
	CollectionTypeTVGenre            CollectionType = 103
	CollectionTypeTVLatest           CollectionType = 104
	CollectionTypeTVNextUp           CollectionType = 105
	CollectionTypeTVResume           CollectionType = 106
	CollectionTypeTVFavoriteSeries   CollectionType = 107
	CollectionTypeTVFavoriteEpisodes CollectionType = 108
	CollectionTypeMovieLatest        CollectionType = 109
	CollectionTypeMovieResume        CollectionType = 110
	CollectionTypeMovieMovies        CollectionType = 111
	CollectionTypeMovieCollection    CollectionType = 112
	CollectionTypeMovieFavorites     CollectionType = 113
	CollectionTypeMovieGenres        CollectionType = 114
	CollectionTypeMovieGenre         CollectionType = 115
)

// String returns the canonical collection type name, or empty for an unknown value.
func (c CollectionType) String() string {
	switch c {
	case CollectionTypeUnknown:
		return "unknown"
	case CollectionTypeMovies:
		return "movies"
	case CollectionTypeTVShows:
		return "tvshows"
	case CollectionTypeMusic:
		return "music"
	case CollectionTypeMusicVideos:
		return "musicvideos"
	case CollectionTypeTrailers:
		return "trailers"
	case CollectionTypeHomeVideos:
		return "homevideos"
	case CollectionTypeBoxSets:
		return "boxsets"
	case CollectionTypeBooks:
		return "books"
	case CollectionTypePhotos:
		return "photos"
	case CollectionTypeLiveTV:
		return "livetv"
	case CollectionTypePlaylists:
		return "playlists"
	case CollectionTypeFolders:
		return "folders"
	case CollectionTypeTVShowSeries:
		return "tvshowseries"
	case CollectionTypeTVGenres:
		return "tvgenres"
	case CollectionTypeTVGenre:
		return "tvgenre"
	case CollectionTypeTVLatest:
		return "tvlatest"
	case CollectionTypeTVNextUp:
		return "tvnextup"
	case CollectionTypeTVResume:
		return "tvresume"
	case CollectionTypeTVFavoriteSeries:
		return "tvfavoriteseries"
	case CollectionTypeTVFavoriteEpisodes:
		return "tvfavoriteepisodes"
	case CollectionTypeMovieLatest:
		return "movielatest"
	case CollectionTypeMovieResume:
		return "movieresume"
	case CollectionTypeMovieMovies:
		return "moviemovies"
	case CollectionTypeMovieCollection:
		return "moviecollection"
	case CollectionTypeMovieFavorites:
		return "moviefavorites"
	case CollectionTypeMovieGenres:
		return "moviegenres"
	case CollectionTypeMovieGenre:
		return "moviegenre"
	default:
		return ""
	}
}

// DayOfWeek matches System.DayOfWeek.
type DayOfWeek uint8

const (
	DayOfWeekSunday DayOfWeek = iota
	DayOfWeekMonday
	DayOfWeekTuesday
	DayOfWeekWednesday
	DayOfWeekThursday
	DayOfWeekFriday
	DayOfWeekSaturday
)

// String returns the canonical day name, or empty for an unknown value.
func (d DayOfWeek) String() string {
	switch d {
	case DayOfWeekSunday:
		return "Sunday"
	case DayOfWeekMonday:
		return "Monday"
	case DayOfWeekTuesday:
		return "Tuesday"
	case DayOfWeekWednesday:
		return "Wednesday"
	case DayOfWeekThursday:
		return "Thursday"
	case DayOfWeekFriday:
		return "Friday"
	case DayOfWeekSaturday:
		return "Saturday"
	default:
		return ""
	}
}

// ImageOrientation matches MediaBrowser.Model.Drawing.ImageOrientation.
type ImageOrientation uint8

const (
	ImageOrientationTopLeft ImageOrientation = 1 + iota
	ImageOrientationTopRight
	ImageOrientationBottomRight
	ImageOrientationBottomLeft
	ImageOrientationLeftTop
	ImageOrientationRightTop
	ImageOrientationRightBottom
	ImageOrientationLeftBottom
)

// String returns the canonical image orientation name, or empty for an unknown value.
func (i ImageOrientation) String() string {
	switch i {
	case ImageOrientationTopLeft:
		return "TopLeft"
	case ImageOrientationTopRight:
		return "TopRight"
	case ImageOrientationBottomRight:
		return "BottomRight"
	case ImageOrientationBottomLeft:
		return "BottomLeft"
	case ImageOrientationLeftTop:
		return "LeftTop"
	case ImageOrientationRightTop:
		return "RightTop"
	case ImageOrientationRightBottom:
		return "RightBottom"
	case ImageOrientationLeftBottom:
		return "LeftBottom"
	default:
		return ""
	}
}
