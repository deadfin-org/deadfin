// SPDX-License-Identifier: GPL-2.0-only

package itemedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// metadataPathToken is the virtual prefix stored for files under the
// internal metadata directory (upstream ApplicationHost virtual paths).
const metadataPathToken = "%MetadataPath%"

// maxImageBytes bounds a decoded upload. Upstream relies on Kestrel's request
// limit (about 28.6 MB of Base64 text).
const maxImageBytes = 32 << 20

// ErrUnsupportedImage reports an image media type with no file extension.
var ErrUnsupportedImage = errors.New("itemedit: unsupported image media type")

// SetImage saves an uploaded image (ImageController.SetItemImage and
// ImageSaver.SaveImage). The image always goes to the item's internal
// metadata directory (divergence D-44). A Backdrop appends at the next index;
// another type replaces index 0. The replaced file is deleted when it was in
// the internal metadata directory, as upstream does when it does not save
// with the media.
func (s *Service) SetImage(ctx context.Context, id dto.UUID, imageType item.ImageType, mediaType string, body io.Reader) error {
	if s.metadataPath == "" {
		return errors.New("itemedit: metadata path is unavailable")
	}
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	extension, ok := imageExtension(mediaType)
	if !ok {
		return ErrUnsupportedImage
	}
	if imageType == item.ImageTypeChapter {
		return fmt.Errorf("%w: chapter images are not set through SetImage", ErrInvalidRequest)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxImageBytes+1))
	if err != nil {
		return fmt.Errorf("%w: image body: %v", ErrInvalidRequest, err)
	}
	if len(data) > maxImageBytes {
		return fmt.Errorf("%w: image body exceeds %d bytes", ErrInvalidRequest, maxImageBytes)
	}

	images := imagesOfType(target.ImageInfos, imageType)
	index := 0
	if allowsMultipleImages(imageType) {
		index = len(images)
	}
	var current *item.ImageInfo
	for position := range target.ImageInfos {
		if target.ImageInfos[position].Type == imageType && target.ImageInfos[position].Index == index {
			current = &target.ImageInfos[position]
		}
	}

	directory := internalMetadataPath(target.ID)
	name := imageSaveName(target, imageType, index, images) + "-" + contentTag(data) + extension
	relative := filepath.Join(directory, name)
	absolute := filepath.Join(s.metadataPath, relative)
	if err := writeImageFile(absolute, data); err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	modified := info.ModTime().UTC()
	stored := filepath.ToSlash(filepath.Join(metadataPathToken, relative))

	previous := ""
	if current != nil {
		previous = current.Path
		current.Path = stored
		current.DateModified = &modified
		current.Width, current.Height = 0, 0
		current.BlurHash = nil
	} else {
		target.ImageInfos = append(target.ImageInfos, item.ImageInfo{Type: imageType, Index: index, Path: stored, DateModified: &modified})
	}
	if err := s.saveImages(ctx, target); err != nil {
		return err
	}
	if previous != "" && previous != stored {
		if expanded := s.paths.Expand(previous); isLocalImage(previous) && s.inMetadataPath(expanded) {
			if err := os.Remove(expanded); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// DeleteImage removes an item image (BaseItem.DeleteImageAsync). A local
// image file is deleted from disk. A missing image is not an error. The
// remaining images of the type keep a dense index order, as upstream's
// positional image list does.
func (s *Service) DeleteImage(ctx context.Context, id dto.UUID, imageType item.ImageType, index int) error {
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	position := imagePosition(target.ImageInfos, imageType, index)
	if position < 0 {
		return nil
	}
	removed := target.ImageInfos[position]
	if isLocalImage(removed.Path) && removed.Path != "" {
		if err := os.Remove(s.paths.Expand(removed.Path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	target.ImageInfos = append(target.ImageInfos[:position:position], target.ImageInfos[position+1:]...)
	for position := range target.ImageInfos {
		if target.ImageInfos[position].Type == imageType && target.ImageInfos[position].Index > removed.Index {
			target.ImageInfos[position].Index--
		}
	}
	return s.saveImages(ctx, target)
}

// RemoveRemoteImage removes the image of imageType whose stored path is path,
// as LibraryManager.ConvertImageToLocal removes a remote image that its source
// no longer serves (BaseItem.RemoveImage). The match is on the path, so a
// second request for the same image does not remove another one. No file is
// deleted. A missing image is not an error.
func (s *Service) RemoveRemoteImage(ctx context.Context, id dto.UUID, imageType item.ImageType, path string) error {
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	position := -1
	for candidate, image := range target.ImageInfos {
		if image.Type == imageType && image.Path == path {
			position = candidate
			break
		}
	}
	if position < 0 {
		return nil
	}
	removed := target.ImageInfos[position]
	target.ImageInfos = append(target.ImageInfos[:position:position], target.ImageInfos[position+1:]...)
	for position := range target.ImageInfos {
		if target.ImageInfos[position].Type == imageType && target.ImageInfos[position].Index > removed.Index {
			target.ImageInfos[position].Index--
		}
	}
	return s.saveImages(ctx, target)
}

// UpdateImageIndex moves an image to a new index (BaseItem.SwapImagesAsync).
// Only types that allow several images (Backdrop) can move. A missing index
// or a remote image is a no-op, as upstream. Deadfin exchanges the two image
// records instead of the file contents, so each image keeps its tag
// (divergence D-44).
func (s *Service) UpdateImageIndex(ctx context.Context, id dto.UUID, imageType item.ImageType, index, newIndex int) error {
	target, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if !allowsMultipleImages(imageType) {
		return fmt.Errorf("%w: the change index operation is only applicable to backdrops and screen shots", ErrInvalidRequest)
	}
	first := imagePosition(target.ImageInfos, imageType, index)
	second := imagePosition(target.ImageInfos, imageType, newIndex)
	if first < 0 || second < 0 {
		return nil
	}
	if !isLocalImage(target.ImageInfos[first].Path) || !isLocalImage(target.ImageInfos[second].Path) {
		return nil
	}
	target.ImageInfos[first].Index, target.ImageInfos[second].Index = target.ImageInfos[second].Index, target.ImageInfos[first].Index
	for _, position := range []int{first, second} {
		target.ImageInfos[position].Width, target.ImageInfos[position].Height = 0, 0
	}
	return s.saveImages(ctx, target)
}

func (s *Service) saveImages(ctx context.Context, target item.Item) error {
	sort.SliceStable(target.ImageInfos, func(left, right int) bool {
		if target.ImageInfos[left].Type != target.ImageInfos[right].Type {
			return target.ImageInfos[left].Type < target.ImageInfos[right].Type
		}
		return target.ImageInfos[left].Index < target.ImageInfos[right].Index
	})
	now := s.clock.Now().UTC()
	target.DateLastSaved = &now
	if err := s.items.EditItems(ctx, []item.Item{target}, nil); err != nil {
		return err
	}
	s.publish([]item.Item{target})
	return nil
}

func (s *Service) inMetadataPath(path string) bool {
	relative, err := filepath.Rel(s.metadataPath, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// internalMetadataPath ports BaseItem.GetInternalMetadataPath relative to the
// metadata root: library/<first two id characters>/<id>.
func internalMetadataPath(id dto.UUID) string {
	value := id.String()
	return filepath.Join("library", value[:2], value)
}

// imageSaveName ports the non-local file names of
// ImageSaver.GetStandardSavePath (ImageSaver.cs:524-575).
func imageSaveName(target item.Item, imageType item.ImageType, index int, existing []item.ImageInfo) string {
	switch imageType {
	case item.ImageTypeArt:
		return "clearart"
	case item.ImageTypeBoxRear:
		return "back"
	case item.ImageTypeThumb:
		return "landscape"
	case item.ImageTypeDisc:
		if target.Kind == item.KindMusicAlbum {
			return "cdart"
		}
		return "disc"
	case item.ImageTypePrimary:
		switch target.Kind {
		case item.KindMusicAlbum, item.KindMusicArtist, item.KindPhotoAlbum, item.KindPerson:
			return "folder"
		}
		return "poster"
	case item.ImageTypeBackdrop:
		return backdropSaveName(existing, index)
	default:
		return strings.ToLower(imageType.String())
	}
}

// backdropSaveName ports GetBackdropSaveFilename: index 0 is "backdrop";
// another index takes the first "backdropN" no existing image uses.
func backdropSaveName(existing []item.ImageInfo, index int) string {
	if index == 0 {
		return "backdrop"
	}
	used := make(map[string]struct{}, len(existing))
	for _, image := range existing {
		name := strings.TrimSuffix(filepath.Base(image.Path), filepath.Ext(image.Path))
		if dash := strings.LastIndexByte(name, '-'); dash > 0 {
			name = name[:dash]
		}
		used[strings.ToLower(name)] = struct{}{}
	}
	for current := 1; ; current++ {
		candidate := "backdrop" + strconv.Itoa(current)
		if _, found := used[candidate]; !found {
			return candidate
		}
	}
}

// contentTag names a saved image by its content, so a replaced image gets a
// new file name and therefore a new image tag (divergence D-12).
func contentTag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4])
}

// imageExtension ports MimeTypes.ToExtension for the image media types
// bind.ImageBody accepts, with ImageSaver's .jpeg → .jpg rule.
func imageExtension(mediaType string) (string, bool) {
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg", "image/pjpeg":
		return ".jpg", true
	case "image/png", "image/x-png":
		return ".png", true
	case "image/apng":
		return ".apng", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	case "image/bmp":
		return ".bmp", true
	case "image/svg+xml":
		return ".svg", true
	case "image/tiff":
		return ".tiff", true
	case "image/avif":
		return ".avif", true
	case "image/heic":
		return ".heic", true
	case "image/heif":
		return ".heif", true
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico", true
	}
	return "", false
}

func writeImageFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if _, err := io.Copy(temporary, bytes.NewReader(data)); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func imagesOfType(images []item.ImageInfo, imageType item.ImageType) []item.ImageInfo {
	var result []item.ImageInfo
	for _, image := range images {
		if image.Type == imageType {
			result = append(result, image)
		}
	}
	return result
}

func imagePosition(images []item.ImageInfo, imageType item.ImageType, index int) int {
	for position, image := range images {
		if image.Type == imageType && image.Index == index {
			return position
		}
	}
	return -1
}

// allowsMultipleImages ports BaseItem.AllowsMultipleImages. Chapter images
// are not item image records in Deadfin.
func allowsMultipleImages(imageType item.ImageType) bool {
	return imageType == item.ImageTypeBackdrop
}

// isLocalImage ports ItemImageInfo.IsLocalFile: a path that is not an http
// or https URL.
func isLocalImage(path string) bool {
	lower := strings.ToLower(path)
	return !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")
}
