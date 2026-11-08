// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/imageserve"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/path"
)

// imageCacheControl is the caching directive applied to served images. Images
// are addressed by an immutable content tag (the ETag), so a long max-age with
// public caching is safe; a changed source produces a new tag and therefore a
// new URL. (Exact-value parity with stock is a T2 differential check.)
const imageCacheControl = "public, max-age=2592000"

// ItemImageReader is the narrow store seam the image routes need: the ordered
// image records for an item. Named-entity (genre/studio/person/artist) images
// use the same seam after a by-name lookup through ItemReader.
type ItemImageReader interface {
	Images(ctx context.Context, id dto.UUID) ([]item.ImageInfo, error)
}

// ImageRenderer renders a selected image into a cache-backed artifact. It is
// nil in deployments constructed without a libvips runtime (for example, the route
// ledger export), where the serving routes remain registered but answer 503.
type ImageRenderer interface {
	Render(ctx context.Context, info item.ImageInfo, request imageserve.Request) (imageserve.Result, error)
}

// RemoteImageRemover removes an item image whose remote source answered
// 404 or 403. *itemedit.Service satisfies it.
type RemoteImageRemover interface {
	RemoveRemoteImage(ctx context.Context, id dto.UUID, imageType item.ImageType, path string) error
}

// RegisterImages registers the item image read surface: the metadata listing,
// and GET/HEAD serving in the query, indexed, and fully path-encoded forms.
//
// Image GET/HEAD are anonymous so <img> tags and native clients load artwork
// without a token (upstream marks these [AllowAnonymous]); the metadata listing
// requires the default authorization policy. Uploads, deletes, reorder, and ByName
// are deferred (see the PR body / WP-P3-03 report). User image reads are registered
//
// remover can be nil. Then a remote image that its source no longer serves
// stays on the item.
func RegisterImages(rt *router.Router, reader ItemImageReader, renderer ImageRenderer, paths path.VirtualPathExpander, remover RemoteImageRemover, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := imageHandler{reader: reader, renderer: renderer, paths: paths, remover: remover, logger: logger}

	rt.Handle(http.MethodGet, "/Items/{itemId}/Images", router.Route{
		OperationID: "GetItemImageInfos",
		Policy:      string(auth.PolicyDefaultAuthorization),
	}, h.list)

	for _, route := range []struct {
		method, pattern, operationID string
	}{
		{http.MethodGet, "/Items/{itemId}/Images/{imageType}", "GetItemImage"},
		{http.MethodHead, "/Items/{itemId}/Images/{imageType}", "HeadItemImage"},
		{http.MethodGet, "/Items/{itemId}/Images/{imageType}/{imageIndex}", "GetItemImageByIndex"},
		{http.MethodHead, "/Items/{itemId}/Images/{imageType}/{imageIndex}", "HeadItemImageByIndex"},
	} {
		rt.Handle(route.method, route.pattern, router.Route{OperationID: route.operationID, Anonymous: true}, h.serve)
	}

	const pathEncoded = "/Items/{itemId}/Images/{imageType}/{imageIndex}/{tag}/{format}/{maxWidth}/{maxHeight}/{percentPlayed}/{unplayedCount}"
	rt.Handle(http.MethodGet, pathEncoded, router.Route{OperationID: "GetItemImage2", Anonymous: true}, h.servePathEncoded)
	rt.Handle(http.MethodHead, pathEncoded, router.Route{OperationID: "HeadItemImage2", Anonymous: true}, h.servePathEncoded)

	// Named-entity images (ImageController GetPersonImage and its siblings)
	// resolve the entity by name and then serve like the item routes.
	names, _ := reader.(ItemReader)
	for _, entity := range []struct {
		prefix, operation string
		kind              item.Kind
		unindexed         bool
	}{
		{"/Persons", "Person", item.KindPerson, true},
		{"/Genres", "Genre", item.KindGenre, true},
		{"/MusicGenres", "MusicGenre", item.KindMusicGenre, true},
		{"/Studios", "Studio", item.KindStudio, true},
		{"/Artists", "Artist", item.KindMusicArtist, false},
	} {
		serve := h.serveNamed(names, entity.kind)
		indexed := entity.prefix + "/{name}/Images/{imageType}/{imageIndex}"
		if entity.unindexed {
			unindexed := entity.prefix + "/{name}/Images/{imageType}"
			rt.Handle(http.MethodGet, unindexed, router.Route{OperationID: "Get" + entity.operation + "Image", Anonymous: true}, serve)
			rt.Handle(http.MethodHead, unindexed, router.Route{OperationID: "Head" + entity.operation + "Image", Anonymous: true}, serve)
			rt.Handle(http.MethodGet, indexed, router.Route{OperationID: "Get" + entity.operation + "ImageByIndex", Anonymous: true}, serve)
			rt.Handle(http.MethodHead, indexed, router.Route{OperationID: "Head" + entity.operation + "ImageByIndex", Anonymous: true}, serve)
			continue
		}
		// The pinned spec has only the indexed artist route, named without
		// the ByIndex suffix.
		rt.Handle(http.MethodGet, indexed, router.Route{OperationID: "Get" + entity.operation + "Image", Anonymous: true}, serve)
		rt.Handle(http.MethodHead, indexed, router.Route{OperationID: "Head" + entity.operation + "Image", Anonymous: true}, serve)
	}
}

// serveNamed serves an image of the by-name entity of kind. An unknown name
// answers 404, as an entity that LibraryManager creates on read has no image.
func (h imageHandler) serveNamed(names ItemReader, kind item.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if names == nil {
			internalError(w)
			return
		}
		imageType, ok := parseImageType(chi.URLParam(r, "imageType"))
		if !ok {
			badRequest(w)
			return
		}
		index, ok := imageIndexParam(w, r)
		if !ok {
			return
		}
		var query imageQuery
		if err := bind.QueryRequest(r, &query); err != nil {
			badRequest(w)
			return
		}
		entity, err := lookupNamedEntity(r.Context(), names, kind, routeName(r, "name"), nil)
		if err != nil {
			internalError(w)
			return
		}
		if entity == nil {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		h.render(w, r, entity.ID, imageType, index, query.toRequest(r))
	}
}

type imageHandler struct {
	reader   ItemImageReader
	renderer ImageRenderer
	paths    path.VirtualPathExpander
	remover  RemoteImageRemover
	logger   *slog.Logger
}

// imageQuery binds the uniform image parameter set. Overlay params are bound so
// requests carrying them are accepted; their rendering lands with WP-P3-02.
type imageQuery struct {
	Format          *string  `query:"format"`
	Quality         *int     `query:"quality"`
	Width           *int     `query:"width"`
	Height          *int     `query:"height"`
	MaxWidth        *int     `query:"maxWidth"`
	MaxHeight       *int     `query:"maxHeight"`
	FillWidth       *int     `query:"fillWidth"`
	FillHeight      *int     `query:"fillHeight"`
	PercentPlayed   *float64 `query:"percentPlayed"`
	UnplayedCount   *int     `query:"unplayedCount"`
	Blur            *int     `query:"blur"`
	BackgroundColor *string  `query:"backgroundColor"`
	ForegroundLayer *string  `query:"foregroundLayer"`
	Tag             *string  `query:"tag"`
}

func (h imageHandler) serve(w http.ResponseWriter, r *http.Request) {
	itemID, ok := imageItemID(w, r)
	if !ok {
		return
	}
	imageType, ok := parseImageType(chi.URLParam(r, "imageType"))
	if !ok {
		badRequest(w)
		return
	}
	index, ok := imageIndexParam(w, r)
	if !ok {
		return
	}

	var query imageQuery
	if err := bind.QueryRequest(r, &query); err != nil {
		badRequest(w)
		return
	}
	h.render(w, r, itemID, imageType, index, query.toRequest(r))
}

func (h imageHandler) servePathEncoded(w http.ResponseWriter, r *http.Request) {
	itemID, ok := imageItemID(w, r)
	if !ok {
		return
	}
	imageType, ok := parseImageType(chi.URLParam(r, "imageType"))
	if !ok {
		badRequest(w)
		return
	}
	index, err := strconv.Atoi(chi.URLParam(r, "imageIndex"))
	if err != nil || index < 0 {
		badRequest(w)
		return
	}

	request := imageserve.Request{Quality: defaultImageQuality, AutoOrient: false}
	if format, ok := parseImageFormat(chi.URLParam(r, "format")); ok {
		request.Format = &format
	}
	maxWidth, ok := pathEncodedDimension(w, chi.URLParam(r, "maxWidth"))
	if !ok {
		return
	}
	request.Resize.MaxWidth = maxWidth
	maxHeight, ok := pathEncodedDimension(w, chi.URLParam(r, "maxHeight"))
	if !ok {
		return
	}
	request.Resize.MaxHeight = maxHeight
	if percent, err := strconv.ParseFloat(chi.URLParam(r, "percentPlayed"), 64); err == nil {
		request.PercentPlayed = percent
	}
	if count, err := strconv.Atoi(chi.URLParam(r, "unplayedCount")); err == nil {
		request.UnplayedCount = &count
	}
	h.render(w, r, itemID, imageType, index, request)
}

// pathEncodedDimension parses a path-segment dimension bound. The segment "0"
// or an empty segment means "unset" — upstream encodes an absent bound as 0 —
// yielding (nil, true); a positive value yields (&v, true). An invalid value
// writes 400 and returns (nil, false), where false means "stop, response sent".
func pathEncodedDimension(w http.ResponseWriter, raw string) (*int, bool) {
	if raw == "" || raw == "0" {
		return nil, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		badRequest(w)
		return nil, false
	}
	return &value, true
}

func (h imageHandler) render(w http.ResponseWriter, r *http.Request, itemID dto.UUID, imageType item.ImageType, index int, request imageserve.Request) {
	if h.reader == nil {
		internalError(w)
		return
	}
	infos, err := h.reader.Images(r.Context(), itemID)
	if err != nil {
		internalError(w)
		return
	}
	info, err := imageserve.Select(infos, imageType, index)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if h.renderer == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	// The ETag must match the listing ImageTag, which imageserve.Tag derives
	// from the raw stored path. Expand the path for file access only.
	rawTag := imageserve.Tag(info.Path)
	storedPath := info.Path
	info.Path = h.paths.Expand(info.Path)
	result, err := h.renderer.Render(r.Context(), info, request)
	if err != nil {
		if errors.Is(err, imageserve.ErrRemoteGone) {
			h.removeGone(r.Context(), itemID, info.Type, storedPath)
		}
		h.renderError(w, err)
		return
	}
	result.Tag = rawTag
	serveImageArtifact(w, r, result)
}

// removeGone removes an image whose remote source answered 404 or 403, so the
// next listing has no tag for it and clients stop asking
// (LibraryManager.ConvertImageToLocal). A failed removal is logged only: the
// remote cache still answers without a fetch.
func (h imageHandler) removeGone(ctx context.Context, itemID dto.UUID, imageType item.ImageType, storedPath string) {
	if h.remover == nil {
		return
	}
	if err := h.remover.RemoveRemoteImage(ctx, itemID, imageType, storedPath); err != nil {
		h.logger.Warn("remove remote image", slog.String("item", itemID.String()), slog.String("type", imageType.String()), slog.Any("error", err))
		return
	}
	h.logger.Info("removed remote image that its source no longer serves", slog.String("item", itemID.String()), slog.String("type", imageType.String()))
}

func (h imageHandler) renderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, imageserve.ErrImageNotFound), errors.Is(err, imageserve.ErrRemoteGone):
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	case errors.Is(err, imageserve.ErrRemoteUnavailable), errors.Is(err, imageserve.ErrProcessorUnavailable), errors.Is(err, imageserve.ErrRemoteFailed):
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	default:
		internalError(w)
	}
}

// serveImageArtifact streams the published cache file. http.ServeContent owns
// range, HEAD, If-Modified-Since, and — because the ETag is set first —
// If-None-Match/304 handling, so a client's conditional GET short-circuits here.
func serveImageArtifact(w http.ResponseWriter, r *http.Request, result imageserve.Result) {
	file, err := os.Open(result.CachePath)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		internalError(w)
		return
	}

	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("ETag", strconv.Quote(result.Tag))
	w.Header().Set("Cache-Control", imageCacheControl)
	http.ServeContent(w, r, filepath.Base(result.CachePath), info.ModTime(), file)
}

func (h imageHandler) list(w http.ResponseWriter, r *http.Request) {
	if h.reader == nil {
		internalError(w)
		return
	}
	itemID, ok := imageItemID(w, r)
	if !ok {
		return
	}
	infos, err := h.reader.Images(r.Context(), itemID)
	if err != nil {
		internalError(w)
		return
	}

	listing := make([]dto.ImageInfo, 0, len(infos))
	for _, info := range infos {
		listing = append(listing, imageInfoDTO(info, h.paths))
	}
	_ = render.JSON(w, r, http.StatusOK, listing)
}

// imageInfoDTO projects a stored image record into the listing DTO. ImageTag
// matches the tag published elsewhere (base name; imageserve.Tag). Size is the
// on-disk byte length for a local file, or 0 for a not-yet-downloaded remote URL.
// The path is expanded so the listing DTO and file-size stat use the configured
// physical path, not a raw virtual token.
func imageInfoDTO(info item.ImageInfo, paths path.VirtualPathExpander) dto.ImageInfo {
	expanded := paths.Expand(info.Path)
	index := info.Index
	out := dto.ImageInfo{
		ImageType:  info.Type.String(),
		ImageIndex: &index,
		ImageTag:   imageserve.Tag(info.Path),
		Path:       expanded,
		Size:       imageFileSize(expanded),
	}
	if info.BlurHash != nil {
		out.BlurHash = *info.BlurHash
	}
	if info.Width > 0 {
		width := info.Width
		out.Width = &width
	}
	if info.Height > 0 {
		height := info.Height
		out.Height = &height
	}
	return out
}

func imageFileSize(path string) int64 {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func imageItemID(w http.ResponseWriter, r *http.Request) (dto.UUID, bool) {
	itemID, err := dto.ParseUUID(chi.URLParam(r, "itemId"))
	if err != nil {
		badRequest(w)
		return dto.NilUUID, false
	}
	return itemID, true
}

func imageIndexParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "imageIndex")
	if raw == "" {
		return 0, true
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 {
		badRequest(w)
		return 0, false
	}
	return index, true
}

const defaultImageQuality = 90

// toRequest resolves the bound query into an imageserve.Request, deriving the
// client format list from Accept when no explicit format is given.
func (q imageQuery) toRequest(r *http.Request) imageserve.Request {
	request := imageserve.Request{
		Quality: defaultImageQuality,
		// AutoOrient is the item-orientation-resolved option: a TopLeft (or
		// EXIF-less) source is served as a byte-copy passthrough, matching the
		// stock corpus's normal serving cases. Per-image EXIF rotation on the
		// serving route is not yet wired (ImageInfo carries no orientation) —
		// see the WP-P3-03 report; the 8 orientation fixtures are validated at
		// the processor level in internal/images.
		AutoOrient: false,
		Resize: images.ResizeOptions{
			Width:      q.Width,
			Height:     q.Height,
			MaxWidth:   q.MaxWidth,
			MaxHeight:  q.MaxHeight,
			FillWidth:  q.FillWidth,
			FillHeight: q.FillHeight,
		},
	}
	if q.Quality != nil {
		request.Quality = *q.Quality
	}
	if q.Format != nil {
		if format, ok := parseImageFormat(*q.Format); ok {
			request.Format = &format
		}
	} else {
		request.ClientFormats = acceptImageFormats(r)
	}
	if q.PercentPlayed != nil {
		request.PercentPlayed = *q.PercentPlayed
	}
	request.UnplayedCount = q.UnplayedCount
	request.Blur = q.Blur
	if q.BackgroundColor != nil {
		request.Background = *q.BackgroundColor
	}
	if q.ForegroundLayer != nil {
		request.Foreground = *q.ForegroundLayer
	}
	return request
}

// parseImageType resolves an ImageType name case-insensitively.
func parseImageType(raw string) (item.ImageType, bool) {
	for value := item.ImageType(0); value <= item.ImageTypeProfile; value++ {
		if strings.EqualFold(value.String(), raw) {
			return value, true
		}
	}
	return 0, false
}

// parseImageFormat maps a format token (query or path segment) to an image
// output format, accepting the jpeg/jpg alias. An empty or unknown token yields
// (_, false) so the caller falls back to Accept-based negotiation.
func parseImageFormat(raw string) (images.Format, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "jpg", "jpeg":
		return images.FormatJPEG, true
	case "png":
		return images.FormatPNG, true
	case "webp":
		return images.FormatWebP, true
	case "gif":
		return images.FormatGIF, true
	case "bmp":
		return images.FormatBMP, true
	case "svg":
		return images.FormatSVG, true
	default:
		return "", false
	}
}

// acceptImageFormats extracts the ordered image output formats a client will
// accept, from every Accept field value. image/* and */* contribute no explicit
// preference (images.SelectFormat falls back to JPEG).
func acceptImageFormats(r *http.Request) []images.Format {
	var formats []images.Format
	seen := map[images.Format]bool{}
	for _, header := range r.Header.Values("Accept") {
		for _, entry := range strings.Split(header, ",") {
			mediaType := strings.TrimSpace(entry)
			if index := strings.IndexByte(mediaType, ';'); index >= 0 {
				mediaType = strings.TrimSpace(mediaType[:index])
			}
			format, ok := acceptFormat(mediaType)
			if !ok || seen[format] {
				continue
			}
			seen[format] = true
			formats = append(formats, format)
		}
	}
	return formats
}

func acceptFormat(mediaType string) (images.Format, bool) {
	switch strings.ToLower(mediaType) {
	case "image/webp":
		return images.FormatWebP, true
	case "image/png":
		return images.FormatPNG, true
	case "image/jpeg", "image/jpg":
		return images.FormatJPEG, true
	case "image/gif":
		return images.FormatGIF, true
	default:
		return "", false
	}
}
