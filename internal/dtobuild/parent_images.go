// SPDX-License-Identifier: GPL-2.0-only

package dtobuild

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// maxParentImageRounds bounds the batched loads of the parent image graph.
// Each round loads one more level of display parents for the whole page; a
// library tree is never this deep.
const maxParentImageRounds = 16

// maxParentWalk bounds an in-memory parent walk against a cycle in stored
// parent links.
const maxParentWalk = 64

// parentImageGraph holds the items, beyond the page itself, that DtoService
// reads to project parent and series images: display parents, series,
// seasons, albums and album artists. The enricher loads it for a complete
// page in batches, one query set per tree level, so no item causes its own
// query.
type parentImageGraph struct {
	items   map[dto.UUID]item.Item
	absent  map[dto.UUID]struct{}
	artists map[string]dto.UUID
	// noArtist holds the clean names that name no MusicArtist.
	noArtist map[string]struct{}
	// discover records the lookups that miss while the enricher loads the
	// graph. Projection runs with discover false.
	discover     bool
	missingIDs   map[dto.UUID]struct{}
	missingNames map[string]struct{}
}

func newParentImageGraph() *parentImageGraph {
	return &parentImageGraph{
		items:        map[dto.UUID]item.Item{},
		absent:       map[dto.UUID]struct{}{},
		artists:      map[string]dto.UUID{},
		noArtist:     map[string]struct{}{},
		missingIDs:   map[dto.UUID]struct{}{},
		missingNames: map[string]struct{}{},
	}
}

func (g *parentImageGraph) add(value item.Item) { g.items[value.ID] = value }

// lookup is LibraryManager.GetItemById for a stored link.
func (g *parentImageGraph) lookup(id *dto.UUID) (item.Item, bool) {
	if g == nil || id == nil || id.IsNil() {
		return item.Item{}, false
	}
	if value, found := g.items[*id]; found {
		return value, true
	}
	if _, found := g.absent[*id]; !found && g.discover {
		g.missingIDs[*id] = struct{}{}
	}
	return item.Item{}, false
}

// artist is LibraryManager.GetArtist: the MusicArtist that the name names.
func (g *parentImageGraph) artist(name string) (item.Item, bool) {
	key := item.CleanName(strings.TrimSpace(name))
	if key == "" {
		return item.Item{}, false
	}
	if id, found := g.artists[key]; found {
		return g.lookup(&id)
	}
	if _, found := g.noArtist[key]; !found && g.discover {
		g.missingNames[key] = struct{}{}
	}
	return item.Item{}, false
}

// findParent is BaseItem.FindParent<T>: the nearest ancestor of kind.
func (g *parentImageGraph) findParent(value item.Item, kind item.Kind) (item.Item, bool) {
	current := value
	for range maxParentWalk {
		parent, found := g.lookup(current.ParentID)
		if !found {
			return item.Item{}, false
		}
		if parent.Kind == kind {
			return parent, true
		}
		current = parent
	}
	return item.Item{}, false
}

// series is Episode.Series and Season.Series.
func (g *parentImageGraph) series(value item.Item) (item.Item, bool) {
	if value.SeriesID != nil && !value.SeriesID.IsNil() {
		series, found := g.lookup(value.SeriesID)
		return series, found && series.Kind == item.KindSeries
	}
	return g.findParent(value, item.KindSeries)
}

// season is Episode.Season.
func (g *parentImageGraph) season(value item.Item) (item.Item, bool) {
	if value.SeasonID != nil && !value.SeasonID.IsNil() {
		season, found := g.lookup(value.SeasonID)
		return season, found && season.Kind == item.KindSeason
	}
	return g.findParent(value, item.KindSeason)
}

// musicArtist is MusicAlbum.GetMusicArtist: the nearest MusicArtist
// ancestor, else the artist named by the first album artist.
func (g *parentImageGraph) musicArtist(album item.Item) (item.Item, bool) {
	if artist, found := g.findParent(album, item.KindMusicArtist); found {
		return artist, true
	}
	if len(album.Values.AlbumArtists) == 0 {
		return item.Item{}, false
	}
	return g.artist(album.Values.AlbumArtists[0])
}

// imageDisplayParent ports DtoService.GetImageDisplayParent. The fallback to
// the first collection folder of an item without a parent is not needed: the
// parent links of every library item end at its CollectionFolder.
func (g *parentImageGraph) imageDisplayParent(current item.Item) (item.Item, bool) {
	if current.Kind == item.KindMusicAlbum {
		if artist, found := g.musicArtist(current); found {
			return artist, true
		}
	}
	if parent, found := g.lookup(displayParentID(current)); found {
		return parent, true
	}
	if owner, found := g.lookup(current.OwnerID); found {
		return owner, true
	}
	return g.lookup(current.ParentID)
}

// displayParentID is BaseItem.DisplayParentId with the Episode and Season
// overrides.
func displayParentID(value item.Item) *dto.UUID {
	switch value.Kind {
	case item.KindEpisode:
		return value.SeasonID
	case item.KindSeason:
		return value.SeriesID
	default:
		return value.ParentID
	}
}

// supportsInheritedParentImages is BaseItem.SupportsInheritedParentImages
// with the overrides of each item class.
func supportsInheritedParentImages(kind item.Kind) bool {
	switch kind {
	case item.KindFolder, item.KindAggregateFolder,
		item.KindVideo, item.KindMovie, item.KindTrailer, item.KindMusicVideo, item.KindEpisode,
		item.KindSeason, item.KindAudio, item.KindAudioBook, item.KindMusicAlbum:
		return true
	default:
		return false
	}
}

// imageLimit is DtoOptions.GetImageLimit.
func imageLimit(options store.ItemQueryDTOOptions, imageType item.ImageType) int {
	if !options.EnableImages {
		return 0
	}
	if len(options.ImageTypes) != 0 && !slices.Contains(options.ImageTypes, imageType) {
		return 0
	}
	return options.ImageTypeLimit
}

// applyParentImages ports the parent and series image fields of
// DtoService.AttachBasicFields: AddInheritedImages, then the Audio, Episode
// and Season blocks. It runs after the item's own images are projected.
func applyParentImages(result *dto.BaseItemDto, value item.Item, graph *parentImageGraph, options store.ItemQueryDTOOptions) {
	addInheritedImages(result, value, graph, options)
	switch value.Kind {
	case item.KindAudio, item.KindAudioBook:
		if album, found := graph.findParent(value, item.KindMusicAlbum); found {
			id := album.ID
			result.AlbumID = &id
			result.AlbumPrimaryImageTag = tagAndFillBlurhash(result, album, item.ImageTypePrimary)
		}
	case item.KindEpisode:
		series, hasSeries := graph.series(value)
		if hasSeries {
			attachSeriesPrimaryImage(result, series)
		}
		if imageLimit(options, item.ImageTypePrimary) > 0 {
			season, hasSeason := graph.season(value)
			seasonTag := ""
			if hasSeason {
				seasonTag = tagAndFillBlurhash(result, season, item.ImageTypePrimary)
			}
			if seasonTag != "" {
				id := season.ID
				result.ParentPrimaryImageItemID = &id
				result.ParentPrimaryImageTag = seasonTag
			} else if hasSeries && result.SeriesPrimaryImageTag != "" {
				id := series.ID
				result.ParentPrimaryImageItemID = &id
				result.ParentPrimaryImageTag = result.SeriesPrimaryImageTag
			}
		}
	case item.KindSeason:
		if series, found := graph.series(value); found {
			attachSeriesPrimaryImage(result, series)
		}
	}
}

// attachSeriesPrimaryImage adds the series poster to an episode or season;
// an item without its own Primary tag takes the series aspect ratio.
func attachSeriesPrimaryImage(result *dto.BaseItemDto, series item.Item) {
	result.SeriesPrimaryImageTag = tagAndFillBlurhash(result, series, item.ImageTypePrimary)
	if _, found := result.ImageTags[item.ImageTypePrimary.String()]; !found {
		result.PrimaryImageAspectRatio = primaryImageAspectRatio(series.ImageInfos)
	}
}

// addInheritedImages ports DtoService.AddInheritedImages.
func addInheritedImages(result *dto.BaseItemDto, value item.Item, graph *parentImageGraph, options store.ItemQueryDTOOptions) {
	if value.Kind == item.KindUserView && value.Extra.UserView != nil &&
		value.Extra.UserView.ViewType != nil && *value.Extra.UserView.ViewType == item.CollectionTypePlaylists &&
		imageLimit(options, item.ImageTypePrimary) > 0 && !value.Extra.UserView.DisplayParentID.IsNil() {
		displayParentID := value.Extra.UserView.DisplayParentID
		if parent, found := graph.lookup(&displayParentID); found {
			if image, found := firstImage(parent, item.ImageTypePrimary); found {
				delete(result.ImageTags, item.ImageTypePrimary.String())
				id := parent.ID
				result.ParentPrimaryImageItemID = &id
				result.ParentPrimaryImageTag = imageTagAndFillBlurhash(result, image)
			}
		}
	}
	if !supportsInheritedParentImages(value.Kind) {
		return
	}
	logoLimit := imageLimit(options, item.ImageTypeLogo)
	thumbLimit := imageLimit(options, item.ImageTypeThumb)
	backdropLimit := imageLimit(options, item.ImageTypeBackdrop)
	// Upstream sets the Art limit to 0: "Emby apps are not using this".
	if logoLimit <= 0 && thumbLimit <= 0 && backdropLimit <= 0 {
		return
	}
	hasOwn := func(imageType item.ImageType) bool {
		_, found := result.ImageTags[imageType.String()]
		return found
	}
	var parent item.Item
	hasParent, isFirst := false, true
	for range maxParentWalk {
		if !((!hasOwn(item.ImageTypeLogo) && logoLimit > 0) || (!hasOwn(item.ImageTypeThumb) && thumbLimit > 0) || (hasParent && parent.Kind == item.KindSeries)) {
			break
		}
		if !hasParent && isFirst {
			parent, hasParent = graph.imageDisplayParent(value)
		}
		if !hasParent {
			break
		}
		if logoLimit > 0 && !hasOwn(item.ImageTypeLogo) && result.ParentLogoItemID == nil {
			if image, found := firstImage(parent, item.ImageTypeLogo); found {
				id := parent.ID
				result.ParentLogoItemID = &id
				result.ParentLogoImageTag = imageTagAndFillBlurhash(result, image)
			}
		}
		if thumbLimit > 0 && !hasOwn(item.ImageTypeThumb) && (result.ParentThumbItemID == nil || parent.Kind == item.KindSeries) &&
			parent.Kind != item.KindCollectionFolder && parent.Kind != item.KindUserView {
			if image, found := firstImage(parent, item.ImageTypeThumb); found {
				id := parent.ID
				result.ParentThumbItemID = &id
				result.ParentThumbImageTag = imageTagAndFillBlurhash(result, image)
			}
		}
		if backdropLimit > 0 && len(result.BackdropImageTags) == 0 && len(result.ParentBackdropImageTags) == 0 {
			images := make([]item.ImageInfo, 0)
			for _, image := range parent.ImageInfos {
				if image.Type == item.ImageTypeBackdrop && len(images) < backdropLimit {
					images = append(images, image)
				}
			}
			if len(images) != 0 {
				tags := make([]string, 0, len(images))
				for _, image := range images {
					if tag := imageTagAndFillBlurhash(result, image); tag != "" {
						tags = append(tags, tag)
					}
				}
				id := parent.ID
				result.ParentBackdropItemID = &id
				result.ParentBackdropImageTags = tags
			}
		}
		isFirst = false
		if !supportsInheritedParentImages(parent.Kind) {
			break
		}
		parent, hasParent = graph.imageDisplayParent(parent)
	}
}

func firstImage(value item.Item, imageType item.ImageType) (item.ImageInfo, bool) {
	for _, image := range value.ImageInfos {
		if image.Type == imageType {
			return image, true
		}
	}
	return item.ImageInfo{}, false
}

// tagAndFillBlurhash is DtoService.GetTagAndFillBlurhash for the first image
// of imageType. It returns "" when value has no such image.
func tagAndFillBlurhash(result *dto.BaseItemDto, value item.Item, imageType item.ImageType) string {
	image, found := firstImage(value, imageType)
	if !found {
		return ""
	}
	return imageTagAndFillBlurhash(result, image)
}

// imageTagAndFillBlurhash returns the image tag and adds the image BlurHash
// to the DTO under its tag.
func imageTagAndFillBlurhash(result *dto.BaseItemDto, image item.ImageInfo) string {
	tag := filepath.Base(image.Path)
	if tag == "" || tag == "." {
		return ""
	}
	if image.BlurHash != nil && *image.BlurHash != "" {
		if result.ImageBlurHashes == nil {
			result.ImageBlurHashes = make(map[string]map[string]string)
		}
		kind := image.Type.String()
		if result.ImageBlurHashes[kind] == nil {
			result.ImageBlurHashes[kind] = make(map[string]string)
		}
		result.ImageBlurHashes[kind][tag] = *image.BlurHash
	}
	return tag
}

// parentImages loads the parent image graph of a page. Each round runs every
// projection of the page against the graph, records the items and artist
// names that are not loaded yet, and loads them in one batch. A round adds
// one level of the tree, so the number of queries follows the tree depth,
// not the page size. The discovery runs with no own images on the item,
// which walks at least as far as the projection does.
func (e storeEnricher) parentImages(ctx context.Context, values []item.Item, related relatedItemGraph, options store.ItemQueryDTOOptions) (*parentImageGraph, error) {
	graph := newParentImageGraph()
	for _, value := range values {
		graph.add(value)
	}
	for _, group := range []map[dto.UUID]item.Item{related.albums, related.seasons, related.series} {
		for _, value := range group {
			graph.add(value)
		}
	}
	graph.discover = true
	defer func() { graph.discover = false }()
	for range maxParentImageRounds {
		for _, value := range values {
			var scratch dto.BaseItemDto
			applyParentImages(&scratch, value, graph, options)
		}
		if len(graph.missingIDs) == 0 && len(graph.missingNames) == 0 {
			break
		}
		if err := e.loadParentImageNames(ctx, graph); err != nil {
			return nil, err
		}
		if err := e.loadParentImageItems(ctx, graph); err != nil {
			return nil, err
		}
	}
	return graph, nil
}

func (e storeEnricher) loadParentImageItems(ctx context.Context, graph *parentImageGraph) error {
	ids := make([]dto.UUID, 0, len(graph.missingIDs))
	for id := range graph.missingIDs {
		ids = append(ids, id)
	}
	clear(graph.missingIDs)
	if len(ids) == 0 {
		return nil
	}
	loaded, err := e.items.GetBatch(ctx, ids)
	if err != nil {
		return err
	}
	for _, value := range loaded {
		graph.add(value)
	}
	for _, id := range ids {
		if _, found := graph.items[id]; !found {
			graph.absent[id] = struct{}{}
		}
	}
	return nil
}

// loadParentImageNames resolves album artist names to MusicArtist items.
// GetArtist returns the artist item accessed by name; when a folder artist
// has the same name, Deadfin keeps only the folder artist (D-111).
func (e storeEnricher) loadParentImageNames(ctx context.Context, graph *parentImageGraph) error {
	names := make([]string, 0, len(graph.missingNames))
	for name := range graph.missingNames {
		names = append(names, name)
	}
	clear(graph.missingNames)
	if len(names) == 0 {
		return nil
	}
	ids, err := e.items.ItemIDsByCleanName(ctx, []item.Kind{item.KindMusicArtist}, names)
	if err != nil {
		return err
	}
	loaded, err := e.items.GetBatch(ctx, ids)
	if err != nil {
		return err
	}
	byName := map[string]item.Item{}
	for _, value := range loaded {
		graph.add(value)
		if value.Kind != item.KindMusicArtist || value.Name == nil {
			continue
		}
		key := item.CleanName(strings.TrimSpace(*value.Name))
		current, found := byName[key]
		if !found || preferArtist(value, current) {
			byName[key] = value
		}
	}
	for _, name := range names {
		if value, found := byName[name]; found {
			graph.artists[name] = value.ID
		} else {
			graph.noArtist[name] = struct{}{}
		}
	}
	return nil
}

// preferArtist orders two artists of one name: the artist accessed by name
// (no parent) first, then the lower ID.
func preferArtist(candidate, current item.Item) bool {
	candidateByName, currentByName := candidate.ParentID == nil, current.ParentID == nil
	if candidateByName != currentByName {
		return candidateByName
	}
	return bytes.Compare(candidate.ID[:], current.ID[:]) < 0
}
