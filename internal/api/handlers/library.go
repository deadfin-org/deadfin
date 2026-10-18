// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/library"
	"github.com/deadfin-org/deadfin/internal/store"
)

// libraryRefreshTrigger is the narrow scanner lifecycle seam required by the
// library refresh operation. Trigger owns any background scan it accepts.
// RefreshStates returns a copy of the live per-folder refresh state keyed by
// the wire ItemId (CollectionFolderID). An empty map means no scan runs.
type libraryRefreshTrigger interface {
	Trigger() error
	RefreshStates() map[dto.UUID]library.FolderRefreshState
}

type libraryTopology interface {
	AddVirtualFolder(context.Context, string, string, library.LibraryOptions) (library.VirtualFolder, error)
	RemoveVirtualFolder(context.Context, dto.UUID) error
	AddMediaPath(context.Context, dto.UUID, string) error
	RemoveMediaPath(context.Context, dto.UUID, string) error
	UpdateLibraryOptions(context.Context, dto.UUID, library.LibraryOptions) error
	VirtualFolders(context.Context) ([]library.VirtualFolder, error)
}

// RegisterLibrary registers library operations on rt.
func RegisterLibrary(rt *router.Router, trigger libraryRefreshTrigger, topology libraryTopology) {
	h := libraryHandler{trigger: trigger, topology: topology}
	rt.Handle(http.MethodPost, "/Library/Refresh", router.Route{
		OperationID: "RefreshLibrary",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.refresh)
	for _, route := range []struct {
		method, pattern, operationID string
		handler                      http.HandlerFunc
	}{
		{http.MethodGet, "/Library/VirtualFolders", "GetVirtualFolders", h.virtualFolders},
		{http.MethodPost, "/Library/VirtualFolders", "AddVirtualFolder", h.addVirtualFolder},
		{http.MethodDelete, "/Library/VirtualFolders", "RemoveVirtualFolder", h.removeVirtualFolder},
		{http.MethodPost, "/Library/VirtualFolders/Paths", "AddMediaPath", h.addMediaPath},
		{http.MethodDelete, "/Library/VirtualFolders/Paths", "RemoveMediaPath", h.removeMediaPath},
		{http.MethodPost, "/Library/VirtualFolders/LibraryOptions", "UpdateLibraryOptions", h.updateLibraryOptions},
	} {
		rt.Handle(route.method, route.pattern, router.Route{
			OperationID: route.operationID,
			Policy:      string(auth.PolicyFirstTimeSetupOrElevated),
		}, route.handler)
	}
}

type libraryHandler struct {
	trigger  libraryRefreshTrigger
	topology libraryTopology
}

type virtualFolderInfo struct {
	Name               string                  `json:"Name"`
	Locations          []string                `json:"Locations"`
	CollectionType     *string                 `json:"CollectionType,omitempty"`
	LibraryOptions     *library.LibraryOptions `json:"LibraryOptions,omitempty"`
	ItemID             *dto.UUID               `json:"ItemId,omitempty"`
	PrimaryImageItemID *dto.UUID               `json:"PrimaryImageItemId,omitempty"`
	RefreshProgress    *float64                `json:"RefreshProgress,omitempty"`
	RefreshStatus      *string                 `json:"RefreshStatus,omitempty"`
}

// collectionTypeOptions mirrors Jellyfin's [FromQuery] CollectionTypeOptions?
// nullable enum binding. Stock's ASP.NET model binding parses the query value
// as CollectionTypeOptions? and silently treats unparseable values as null
// (no 400): omission, empty, and invalid values all create no .collection
// marker. Only the eight valid enum values write a marker. This type reproduces
// that contract: it accepts any text without error, canonicalizes valid values
// to lowercase, and leaves invalid/omitted values empty (null).
//
// Stock's enum binding also accepts the base-10 ordinals 0 through 7 that map
// to the same canonical names. This endpoint keeps its own ordinal switch and
// does not import internal/item, whose CollectionType ordinals differ.
// 0=movies, 1=tvshows, 2=music, 3=musicvideos, 4=homevideos, 5=boxsets,
// 6=books, 7=mixed. Parse errors and other ordinals leave the value empty.
type collectionTypeOptions struct {
	value string
	set   bool
}

// numericCollectionTypeName maps a base-10 ordinal that stock accepts for
// CollectionTypeOptions to its canonical lowercase name. It is separate
// from internal/item.CollectionType, whose ordinals differ. The second
// return value is false when the ordinal is outside the 0 through 7 range.
func numericCollectionTypeName(ord int32) (string, bool) {
	switch ord {
	case 0:
		return "movies", true
	case 1:
		return "tvshows", true
	case 2:
		return "music", true
	case 3:
		return "musicvideos", true
	case 4:
		return "homevideos", true
	case 5:
		return "boxsets", true
	case 6:
		return "books", true
	case 7:
		return "mixed", true
	}
	return "", false
}

func (c *collectionTypeOptions) UnmarshalText(text []byte) error {
	c.set = true
	normalized := strings.ToLower(strings.TrimSpace(string(text)))
	switch normalized {
	case "movies", "tvshows", "music", "musicvideos", "homevideos", "boxsets", "books", "mixed":
		c.value = normalized
		return nil
	}
	if n, err := strconv.ParseInt(normalized, 10, 32); err == nil {
		if name, ok := numericCollectionTypeName(int32(n)); ok {
			c.value = name
		}
	}
	return nil
}

type addVirtualFolderQuery struct {
	Name           string                `query:"name"`
	CollectionType collectionTypeOptions `query:"collectionType"`
	Paths          []string              `query:"paths"`
	RefreshLibrary bool                  `query:"refreshLibrary"`
}

type addVirtualFolderRequest struct {
	LibraryOptions *library.LibraryOptions `json:"LibraryOptions"`
}

type virtualFolderMutationQuery struct {
	Name           string `query:"name"`
	RefreshLibrary bool   `query:"refreshLibrary"`
}

type mediaPathRequest struct {
	Name     string                 `json:"Name"`
	Path     *string                `json:"Path"`
	PathInfo *library.MediaPathInfo `json:"PathInfo"`
}

type removeMediaPathQuery struct {
	Name           string `query:"name"`
	Path           string `query:"path"`
	RefreshLibrary bool   `query:"refreshLibrary"`
}

type updateLibraryOptionsRequest struct {
	ID             dto.UUID                `json:"Id"`
	LibraryOptions *library.LibraryOptions `json:"LibraryOptions"`
}

var errAmbiguousVirtualFolder = errors.New("ambiguous case-insensitive virtual-folder name")

func (h libraryHandler) refresh(w http.ResponseWriter, _ *http.Request) {
	if h.trigger == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	switch err := h.trigger.Trigger(); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, library.ErrScannerClosed):
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// projectLibraryOptions copies options and normalizes each nil collection
// field to a nonnil empty slice. Stock returns nonnull JSON arrays for the
// empty LibraryOptions collections. The topology XML decoder leaves an
// omitted collection as nil, and encoding/json projects a nil slice as null.
// The response projection must convert nil to an empty slice so the read-back
// of an empty collection returns [] rather than null. Nonempty collections and
// all scalar fields pass through unchanged. TypeOptions is cloned before its
// entries are normalized so the topology value is not mutated.
func projectLibraryOptions(options library.LibraryOptions) library.LibraryOptions {
	clone := options
	clone.PathInfos = nonnilSlice(options.PathInfos)
	clone.MetadataSavers = nonnilSlice(options.MetadataSavers)
	clone.DisabledLocalMetadataReaders = nonnilSlice(options.DisabledLocalMetadataReaders)
	clone.LocalMetadataReaderOrder = nonnilSlice(options.LocalMetadataReaderOrder)
	clone.DisabledSubtitleFetchers = nonnilSlice(options.DisabledSubtitleFetchers)
	clone.SubtitleFetcherOrder = nonnilSlice(options.SubtitleFetcherOrder)
	clone.DisabledMediaSegmentProviders = nonnilSlice(options.DisabledMediaSegmentProviders)
	clone.MediaSegmentProviderOrder = nonnilSlice(options.MediaSegmentProviderOrder)
	clone.SubtitleDownloadLanguages = nonnilSlice(options.SubtitleDownloadLanguages)
	clone.DisabledLyricFetchers = nonnilSlice(options.DisabledLyricFetchers)
	clone.LyricFetcherOrder = nonnilSlice(options.LyricFetcherOrder)
	clone.CustomTagDelimiters = nonnilSlice(options.CustomTagDelimiters)
	clone.DelimiterWhitelist = nonnilSlice(options.DelimiterWhitelist)
	if options.TypeOptions != nil {
		clone.TypeOptions = make([]library.TypeOptions, len(options.TypeOptions))
		for i, entry := range options.TypeOptions {
			clone.TypeOptions[i] = library.TypeOptions{
				Type:                     entry.Type,
				MetadataFetchers:         nonnilSlice(entry.MetadataFetchers),
				MetadataFetcherOrder:     nonnilSlice(entry.MetadataFetcherOrder),
				ImageFetchers:            nonnilSlice(entry.ImageFetchers),
				ImageFetcherOrder:        nonnilSlice(entry.ImageFetcherOrder),
				ImageOptions:             nonnilSlice(entry.ImageOptions),
				SimilarItemProviders:     nonnilSlice(entry.SimilarItemProviders),
				SimilarItemProviderOrder: nonnilSlice(entry.SimilarItemProviderOrder),
			}
		}
	} else {
		clone.TypeOptions = []library.TypeOptions{}
	}
	return clone
}

// nonnilSlice returns a nonnil empty slice when the input is nil and returns
// the input unchanged when it is nonnil. This makes encoding/json project an
// empty collection as [] rather than null.
func nonnilSlice[T any](collection []T) []T {
	if collection == nil {
		return []T{}
	}
	return collection
}

func (h libraryHandler) virtualFolders(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	// Read the refresh snapshot before the topology list so the projection
	// matches stock ordering: the snapshot is a point-in-time view that the
	// topology list cannot interleave with.
	var refreshStates map[dto.UUID]library.FolderRefreshState
	if h.trigger != nil {
		refreshStates = h.trigger.RefreshStates()
	}
	folders, err := h.topology.VirtualFolders(r.Context())
	if err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	response := make([]virtualFolderInfo, len(folders))
	for index, folder := range folders {
		locations := append([]string(nil), folder.MediaPaths...)
		if locations == nil {
			locations = []string{}
		}
		info := virtualFolderInfo{
			Name:      folder.Name,
			Locations: locations,
			ItemID:    &folder.CollectionFolderID,
		}
		if folder.CollectionType != "" {
			collectionType := folder.CollectionType
			info.CollectionType = &collectionType
		}
		options := projectLibraryOptions(folder.Options)
		info.LibraryOptions = &options
		projectVirtualFolderRefresh(&info, refreshStates[folder.CollectionFolderID])
		response[index] = info
	}
	if err := render.JSON(w, r, http.StatusOK, response); err != nil {
		return
	}
}

func (h libraryHandler) addVirtualFolder(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	var query addVirtualFolderQuery
	if err := bind.QueryRequest(r, &query); err != nil || query.Name == "" || strings.TrimSpace(query.Name) != query.Name {
		writeLibraryRequestError(w, err)
		return
	}
	var request addVirtualFolderRequest
	if err := bind.JSONBody(r, &request); err != nil {
		writeLibraryRequestError(w, err)
		return
	}
	// Upstream: libraryOptionsDto?.LibraryOptions ?? new LibraryOptions().
	options := library.DefaultLibraryOptions()
	if request.LibraryOptions != nil {
		options = *request.LibraryOptions
	}
	if len(query.Paths) > 0 {
		options.PathInfos = make([]library.MediaPathInfo, len(query.Paths))
		for index, path := range query.Paths {
			options.PathInfos[index] = library.MediaPathInfo{Path: path}
		}
	}
	if _, err := h.topology.AddVirtualFolder(r.Context(), query.Name, query.CollectionType.value, options); err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if query.RefreshLibrary && !h.triggerLibraryRefresh(w) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h libraryHandler) removeVirtualFolder(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	var query virtualFolderMutationQuery
	if err := bind.QueryRequest(r, &query); err != nil || strings.TrimSpace(query.Name) == "" {
		writeLibraryRequestError(w, err)
		return
	}
	id, err := h.virtualFolderID(r.Context(), query.Name)
	if err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if err := h.topology.RemoveVirtualFolder(r.Context(), id); err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if query.RefreshLibrary && !h.triggerLibraryRefresh(w) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h libraryHandler) addMediaPath(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	var query struct {
		RefreshLibrary bool `query:"refreshLibrary"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		writeLibraryRequestError(w, err)
		return
	}
	var request *mediaPathRequest
	if err := bind.JSONBody(r, &request); err != nil {
		writeLibraryRequestError(w, err)
		return
	}
	if request == nil || strings.TrimSpace(request.Name) == "" {
		writeLibraryRequestError(w, nil)
		return
	}
	mediaPath := ""
	if request.PathInfo != nil {
		mediaPath = request.PathInfo.Path
	} else if request.Path != nil {
		mediaPath = *request.Path
	}
	if strings.TrimSpace(mediaPath) == "" {
		writeLibraryRequestError(w, nil)
		return
	}
	id, err := h.virtualFolderID(r.Context(), request.Name)
	if err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if err := h.topology.AddMediaPath(r.Context(), id, mediaPath); err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if query.RefreshLibrary && !h.triggerLibraryRefresh(w) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h libraryHandler) removeMediaPath(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	var query removeMediaPathQuery
	if err := bind.QueryRequest(r, &query); err != nil || strings.TrimSpace(query.Name) == "" || strings.TrimSpace(query.Path) == "" {
		writeLibraryRequestError(w, err)
		return
	}
	id, err := h.virtualFolderID(r.Context(), query.Name)
	if err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if err := h.topology.RemoveMediaPath(r.Context(), id, query.Path); err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	if query.RefreshLibrary && !h.triggerLibraryRefresh(w) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h libraryHandler) updateLibraryOptions(w http.ResponseWriter, r *http.Request) {
	if !h.requireTopology(w) {
		return
	}
	var request *updateLibraryOptionsRequest
	if err := bind.JSONBody(r, &request); err != nil {
		writeLibraryRequestError(w, err)
		return
	}
	if request == nil || request.LibraryOptions == nil {
		writeLibraryRequestError(w, nil)
		return
	}
	if err := h.topology.UpdateLibraryOptions(r.Context(), request.ID, *request.LibraryOptions); err != nil {
		writeLibraryTopologyError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h libraryHandler) virtualFolderID(ctx context.Context, name string) (dto.UUID, error) {
	folders, err := h.topology.VirtualFolders(ctx)
	if err != nil {
		return dto.NilUUID, err
	}
	var folded dto.UUID
	foldedMatches := 0
	for _, folder := range folders {
		if folder.Name == name {
			return folder.ID, nil
		}
		if strings.EqualFold(folder.Name, name) {
			folded = folder.ID
			foldedMatches++
		}
	}
	switch foldedMatches {
	case 0:
		return dto.NilUUID, store.ErrNotFound
	case 1:
		return folded, nil
	default:
		return dto.NilUUID, errAmbiguousVirtualFolder
	}
}

func (h libraryHandler) triggerLibraryRefresh(w http.ResponseWriter) bool {
	if h.trigger == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return false
	}
	if err := h.trigger.Trigger(); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, library.ErrScannerClosed) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, http.StatusText(status), status)
		return false
	}
	return true
}

func (h libraryHandler) requireTopology(w http.ResponseWriter) bool {
	if h.topology != nil {
		return true
	}
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
	return false
}

// projectVirtualFolderRefresh sets the wire RefreshStatus and RefreshProgress
// fields for one folder from its live refresh state. Active folders carry a
// numeric RefreshProgress (including zero). Queued folders omit progress.
// Absent or unknown entries project Idle without progress, matching stock.
func projectVirtualFolderRefresh(info *virtualFolderInfo, state library.FolderRefreshState) {
	switch state.Status {
	case library.FolderRefreshActive:
		status := "Active"
		info.RefreshStatus = &status
		progress := state.Progress
		info.RefreshProgress = &progress
	case library.FolderRefreshQueued:
		status := "Queued"
		info.RefreshStatus = &status
	default:
		status := "Idle"
		info.RefreshStatus = &status
	}
}
func writeLibraryRequestError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, bind.ErrUnsupportedMediaType) {
		status = http.StatusUnsupportedMediaType
	}
	http.Error(w, http.StatusText(status), status)
}

func writeLibraryTopologyError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, errAmbiguousVirtualFolder):
		status = http.StatusConflict
	case errors.Is(err, library.ErrTopologyClosed):
		status = http.StatusServiceUnavailable
	}
	http.Error(w, http.StatusText(status), status)
}

// mediaFolderItems reads the library roots of GET /Library/MediaFolders.
type mediaFolderItems interface {
	Query(context.Context, store.ItemQuery) (store.ItemQueryResult, error)
}

// RegisterMediaFolders registers GET /Library/MediaFolders
// (LibraryController.GetMediaFolders, RequiresElevation).
func RegisterMediaFolders(rt *router.Router, topology libraryTopology, items mediaFolderItems, build ItemDTOBuilder) {
	h := mediaFoldersHandler{topology: topology, items: items, build: build}
	rt.Handle(http.MethodGet, "/Library/MediaFolders", router.Route{
		OperationID: "GetMediaFolders",
		Policy:      string(auth.PolicyRequiresElevation),
	}, h.get)
}

type mediaFoldersHandler struct {
	topology libraryTopology
	items    mediaFolderItems
	build    ItemDTOBuilder
}

// get ports GetMediaFolders: the user root's collection folders and the root
// folder's virtual children (the playlists folder), with enabled library
// options, ordered by SortName, filtered by isHidden, projected with default
// DtoOptions and no user.
func (h mediaFoldersHandler) get(w http.ResponseWriter, r *http.Request) {
	if h.topology == nil || h.items == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	var query struct {
		IsHidden *bool `query:"isHidden"`
	}
	if err := bind.QueryRequest(r, &query); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	folders, err := h.topology.VirtualFolders(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	disabled := make(map[dto.UUID]bool, len(folders))
	for _, folder := range folders {
		if !folder.Options.Enabled {
			disabled[folder.CollectionFolderID] = true
		}
	}
	result, err := h.items.Query(r.Context(), store.ItemQuery{
		IncludeItemTypes: []item.Kind{item.KindCollectionFolder, item.KindPlaylistsFolder},
	})
	if err != nil {
		internalError(w)
		return
	}
	values := make([]item.Item, 0, len(result.Items))
	for _, value := range result.Items {
		if disabled[value.ID] {
			continue
		}
		// BaseItem.IsHidden is false; only PlaylistsFolder overrides it.
		hidden := value.Kind == item.KindPlaylistsFolder
		if query.IsHidden != nil && hidden != *query.IsHidden {
			continue
		}
		values = append(values, value)
	}
	sort.SliceStable(values, func(i, j int) bool {
		left, right := strings.ToLower(mediaFolderSortName(values[i])), strings.ToLower(mediaFolderSortName(values[j]))
		if left != right {
			return left < right
		}
		return values[i].ID.String() < values[j].ID.String()
	})
	options := bind.DefaultItemQueryDTOOptions()
	options.EnableUserData = false
	page, err := h.build.BuildDetailed(r.Context(), values, options, nil)
	if err != nil {
		internalError(w)
		return
	}
	page.StartIndex = 0
	page.TotalRecordCount = len(page.Items)
	_ = render.JSON(w, r, http.StatusOK, page)
}

// mediaFolderSortName is BaseItem.SortName: ForcedSortName, else the stored
// sort name, else the name.
func mediaFolderSortName(value item.Item) string {
	for _, name := range []*string{value.ForcedSortName, value.SortName, value.Name} {
		if name != nil && *name != "" {
			return *name
		}
	}
	return ""
}
