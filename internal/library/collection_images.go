// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"fmt"
	"time"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/store"
)

// CollectionImageCreator creates the Primary collage of a CollectionFolder
// (CollectionFolderImageProvider). IsDynamicImage reports whether an image
// is a file in the internal metadata directory of target, which the provider
// may replace.
type CollectionImageCreator interface {
	CreateCollectionFolderImage(ctx context.Context, target *item.Item, itemsWithImages []item.Item) (bool, error)
	IsDynamicImage(target item.Item, image item.ImageInfo) bool
}

// collectionImageMaxAge is CollectionFolderImageProvider.HasChangedByDate:
// a generated library image is created again after seven days.
const collectionImageMaxAge = 7 * 24 * time.Hour

// refreshCollectionFolderImages runs the CollectionFolder image provider for
// each scanned library after the scan stored its items.
func (s *Scanner) refreshCollectionFolderImages(ctx context.Context, folders []VirtualFolder) []RefreshProgress {
	creator, ok := s.options.DynamicImages.(CollectionImageCreator)
	if !ok {
		return nil
	}
	var failed []RefreshProgress
	for _, folder := range folders {
		if err := ctx.Err(); err != nil {
			return failed
		}
		if err := s.refreshCollectionFolderImage(ctx, creator, folder.CollectionFolderID); err != nil {
			failed = append(failed, RefreshProgress{ItemID: folder.CollectionFolderID, Path: folder.Path, Status: "failed", Error: fmt.Errorf("library image: %w", err)})
		}
	}
	return failed
}

// refreshCollectionFolderImage ports BaseDynamicImageProvider.HasChanged and
// FetchAsync for the Primary image of one CollectionFolder.
func (s *Scanner) refreshCollectionFolderImage(ctx context.Context, creator CollectionImageCreator, folderID dto.UUID) error {
	loaded, err := s.items.GetBatch(ctx, []dto.UUID{folderID})
	if err != nil {
		return err
	}
	if len(loaded) == 0 || loaded[0].Kind != item.KindCollectionFolder {
		return nil
	}
	folder := loaded[0]
	for _, image := range folder.ImageInfos {
		if image.Type != item.ImageTypePrimary {
			continue
		}
		// An image that the provider did not create stays.
		if !creator.IsDynamicImage(folder, image) {
			return nil
		}
		if image.DateModified != nil && s.options.Now().Sub(*image.DateModified) <= collectionImageMaxAge {
			return nil
		}
		break
	}
	itemsWithImages, err := s.collectionItemsWithImages(ctx, folder)
	if err != nil || len(itemsWithImages) == 0 {
		return err
	}
	created, err := creator.CreateCollectionFolderImage(ctx, &folder, itemsWithImages)
	if err != nil || !created {
		return err
	}
	saved := scannerLifecycleTimestamp(s.options.Now)
	folder.DateLastSaved = &saved
	if err := s.storeWrite(ctx, func() error { return s.items.EditItems(ctx, []item.Item{folder}, nil) }); err != nil {
		return err
	}
	if folder.ParentID != nil {
		s.changes.publish(*folder.ParentID, folder.ID, store.ReconcileChildrenResult{Updated: []dto.UUID{folder.ID}})
	}
	return nil
}

// collectionItemsWithImages ports CollectionFolderImageProvider.GetItemsWithImages:
// up to eight random items of the library type that have a Primary image.
func (s *Scanner) collectionItemsWithImages(ctx context.Context, folder item.Item) ([]item.Item, error) {
	collectionType := item.CollectionTypeUnknown
	if folder.Extra.CollectionFolder != nil && folder.Extra.CollectionFolder.CollectionType != nil {
		collectionType = *folder.Extra.CollectionFolder.CollectionType
	}
	kinds := collectionTypeItemKinds(collectionType)
	// Music albums usually have no backdrop, so a music library uses artists.
	if collectionType == item.CollectionTypeMusic {
		kinds = []item.Kind{item.KindMusicArtist}
	}
	limit := 8
	result, err := s.items.Query(ctx, store.ItemQuery{
		ParentID:         &folder.ID,
		Recursive:        collectionType != item.CollectionTypePlaylists,
		IncludeItemTypes: kinds,
		ImageTypes:       []item.ImageType{item.ImageTypePrimary},
		SortBy:           []store.ItemSortBy{store.ItemSortByRandom},
		Limit:            &limit,
	})
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

// collectionTypeItemKinds ports DtoExtensions.GetBaseItemKindsForCollectionType.
func collectionTypeItemKinds(collectionType item.CollectionType) []item.Kind {
	switch collectionType {
	case item.CollectionTypeMovies:
		return []item.Kind{item.KindMovie}
	case item.CollectionTypeTVShows:
		return []item.Kind{item.KindSeries}
	case item.CollectionTypeMusic:
		return []item.Kind{item.KindMusicAlbum}
	case item.CollectionTypeMusicVideos:
		return []item.Kind{item.KindMusicVideo}
	case item.CollectionTypeBooks:
		return []item.Kind{item.KindBook, item.KindAudioBook}
	case item.CollectionTypeBoxSets:
		return []item.Kind{item.KindBoxSet}
	case item.CollectionTypeHomeVideos, item.CollectionTypePhotos:
		return []item.Kind{item.KindVideo, item.KindPhoto}
	case item.CollectionTypeFolders:
		return nil
	default:
		return []item.Kind{item.KindVideo, item.KindAudio, item.KindPhoto, item.KindMovie, item.KindSeries}
	}
}
