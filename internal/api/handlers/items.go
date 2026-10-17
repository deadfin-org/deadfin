// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/render"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/go-chi/chi/v5"
)

// ItemReader is the narrow read seam required by the item page endpoint.
type ItemReader interface {
	Query(context.Context, store.ItemQuery) (store.ItemQueryResult, error)
}

// ItemDTOBuilder projects list pages and source-default item details. Every
// adapter must implement both paths so direct item routes cannot fall back to
// sparse list projection.
type ItemDTOBuilder interface {
	Build(context.Context, []item.Item, store.ItemQueryDTOOptions, *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error)
	BuildDetailed(context.Context, []item.Item, store.ItemQueryDTOOptions, *dto.UUID) (dto.QueryResult[dto.BaseItemDto], error)
}

// PersonRefresher is the on-demand Person refresh of GetItem
// (UserLibraryController.RefreshItemOnDemandIfNeeded). *people.Service
// satisfies it.
type PersonRefresher interface {
	RefreshOnDemand(context.Context, item.Item) (item.Item, bool, error)
}

// RegisterItems registers the implemented Items page endpoint and its legacy
// user-scoped alias. Additional item operations register only with their real
// implementations rather than sharing a placeholder handler.
func RegisterItems(rt *router.Router, reader ItemReader, build ItemDTOBuilder, clock clock.Clock, dependencies ...any) {
	if clock == nil {
		panic("handlers: item clock is required")
	}
	var latestPreferences LatestUserPreferences
	var users ItemUserResolver
	var languages LanguageInfoFinder
	var downloadFiles playback.DownloadFileSourceService
	var configuration ShowsConfiguration
	var people PersonRefresher
	for _, dependency := range dependencies {
		switch value := dependency.(type) {
		case ItemServices:
			users = value.Users
			latestPreferences = value.LatestPreferences
			languages = value.Languages
		case playback.DownloadFileSourceService:
			downloadFiles = value
		case LatestUserPreferences:
			latestPreferences = value
		case ShowsConfiguration:
			configuration = value
		case PersonRefresher:
			people = value
		}
	}
	h := itemHandler{reader: reader, build: build, clock: clock, users: users, latestPreferences: latestPreferences, configuration: configuration, people: people}
	userData, _ := reader.(UserDataMutator)
	policy := string(auth.PolicyDefaultAuthorization)
	rt.Handle(http.MethodGet, "/Items", router.Route{OperationID: "GetItems", Policy: policy}, h.items)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items", router.Route{OperationID: "SHADOW:GetItemsLegacy", Policy: policy}, h.legacyItems)
	rt.Handle(http.MethodGet, "/Items/Counts", router.Route{OperationID: "GetItemCounts", Policy: policy}, h.itemCounts)
	rt.Handle(http.MethodGet, "/Items/{itemId}", router.Route{OperationID: "GetItem", Policy: policy}, h.item)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/{itemId}", router.Route{OperationID: "SHADOW:GetItemLegacy", Policy: policy}, h.legacyItem)
	rt.Handle(http.MethodGet, "/UserItems/Resume", router.Route{OperationID: "GetResumeItems", Policy: policy}, h.resume)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/Resume", router.Route{OperationID: "SHADOW:GetResumeItemsLegacy", Policy: policy}, h.legacyResume)
	rt.Handle(http.MethodGet, "/Items/Latest", router.Route{OperationID: "GetLatestMedia", Policy: policy}, h.latest)
	rt.Handle(http.MethodGet, "/Users/{userId}/Items/Latest", router.Route{OperationID: "SHADOW:GetLatestMediaLegacy", Policy: policy}, h.legacyLatest)
	rt.Handle(http.MethodGet, "/Search/Hints", router.Route{OperationID: "GetSearchHints", Policy: policy}, h.searchHints)
	RegisterItemDownloads(rt, downloadFiles)
	RegisterItemExtras(rt, reader, build, NewLocalSimilarItemsService(reader), users)
	RegisterItemNames(rt, reader, build, clock, users, languages)
	RegisterUserData(rt, userData, reader, users, clock)
}

type itemHandler struct {
	reader            ItemReader
	build             ItemDTOBuilder
	clock             clock.Clock
	users             ItemUserResolver
	latestPreferences LatestUserPreferences
	configuration     ShowsConfiguration
	people            PersonRefresher
}

func (h itemHandler) items(w http.ResponseWriter, request *http.Request) {
	user, err := bind.QueryScalar(request, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	h.itemsForUser(w, request, user)
}

func (h itemHandler) legacyItems(w http.ResponseWriter, request *http.Request) {
	h.itemsForUser(w, request, chi.URLParam(request, "userId"))
}

func (h itemHandler) item(w http.ResponseWriter, request *http.Request) {
	user, err := bind.QueryScalar(request, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	h.itemForUser(w, request, user)
}

func (h itemHandler) legacyItem(w http.ResponseWriter, request *http.Request) {
	h.itemForUser(w, request, chi.URLParam(request, "userId"))
}

func (h itemHandler) itemForUser(w http.ResponseWriter, request *http.Request, user string) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	itemID, err := dto.ParseUUID(chi.URLParam(request, "itemId"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	query, err := bind.ItemsQueryRequest(request, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !h.applyRequestUser(w, request, user, &query) {
		return
	}
	if itemID.IsNil() {
		query.ItemIDs = nil
		query.IncludeItemTypes = []item.Kind{item.KindUserRootFolder}
	} else {
		query.ItemIDs = []dto.UUID{itemID}
	}
	result, err := h.reader.Query(request.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if len(result.Items) == 0 {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if refreshed, ok := h.refreshPersonOnDemand(request.Context(), result.Items[0], query); ok {
		result.Items[0] = refreshed
	}
	detailOptions := dtobuild.DetailedOptions(query.DtoOptions)
	page, err := h.build.BuildDetailed(request.Context(), result.Items, detailOptions, query.UserID)
	if err != nil || len(page.Items) == 0 {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	_ = render.JSON(w, request, http.StatusOK, page.Items[0])
}

// refreshPersonOnDemand ports the RefreshItemOnDemandIfNeeded call of
// GetItem: upstream awaits the refresh of a Person without metadata before
// it builds the response. It reads the item again through the query so the
// response carries the refreshed row. A failed refresh serves the stored
// item.
func (h itemHandler) refreshPersonOnDemand(ctx context.Context, value item.Item, query store.ItemQuery) (item.Item, bool) {
	if h.people == nil || value.Kind != item.KindPerson {
		return value, false
	}
	if _, refreshed, err := h.people.RefreshOnDemand(ctx, value); err != nil || !refreshed {
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "person refresh failed", slog.String("item", value.ID.String()), slog.String("error", err.Error()))
		}
		return value, false
	}
	query.ItemIDs = []dto.UUID{value.ID}
	result, err := h.reader.Query(ctx, query)
	if err != nil || len(result.Items) == 0 {
		return value, false
	}
	return result.Items[0], true
}

func (h itemHandler) resume(w http.ResponseWriter, request *http.Request) {
	user, err := bind.QueryScalar(request, "userId")
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	h.resumeForUser(w, request, user)
}

func (h itemHandler) legacyResume(w http.ResponseWriter, request *http.Request) {
	h.resumeForUser(w, request, chi.URLParam(request, "userId"))
}

func (h itemHandler) resumeForUser(w http.ResponseWriter, request *http.Request, user string) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	query, err := bind.ItemsQueryRequest(request, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if !h.applyRequestUser(w, request, user, &query) {
		return
	}
	trueValue, falseValue := true, false
	query.IsResumable = &trueValue
	query.Recursive = true
	query.IncludeOwnedItems = true
	query.CollapseBoxSetItems = &falseValue
	query.SortBy = []store.ItemSortBy{store.ItemSortByDatePlayed}
	query.SortOrder = []store.SortOrder{store.SortOrderDescending}
	result, err := h.reader.Query(request.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	result.Items = filterLatestExcludedItems(result.Items, query.User)
	page, err := h.build.Build(request.Context(), result.Items, query.DtoOptions, query.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page.TotalRecordCount = result.TotalRecordCount
	if query.StartIndex != nil {
		page.StartIndex = *query.StartIndex
	}
	_ = render.JSON(w, request, http.StatusOK, page)
}

func (h itemHandler) itemsForUser(w http.ResponseWriter, request *http.Request, user string) {
	if h.reader == nil || h.build == nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	query, err := bind.ItemsQueryRequest(request, h.clock)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	// ItemsController must return the concrete matches selected by a search,
	// rather than replace them with their containing BoxSet.
	if strings.TrimSpace(query.SearchTerm) != "" {
		collapseBoxSetItems := false
		query.CollapseBoxSetItems = &collapseBoxSetItems
	}
	if !h.applyRequestUser(w, request, user, &query) {
		return
	}
	// A Season parent lists Season.GetEpisodes, which reads this option.
	if h.configuration != nil {
		query.ExcludeSpecialsFromSeasons = !h.configuration.System().DisplaySpecialsWithinSeasons
	}
	result, err := h.reader.Query(request.Context(), query)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page, err := h.build.Build(request.Context(), result.Items, query.DtoOptions, query.UserID)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	page.TotalRecordCount = result.TotalRecordCount
	if query.StartIndex != nil {
		page.StartIndex = *query.StartIndex
	}
	_ = render.JSON(w, request, http.StatusOK, page)
}

func (h itemHandler) applyRequestUser(w http.ResponseWriter, request *http.Request, requested string, query *store.ItemQuery) bool {
	if h.users == nil {
		if requested == "" {
			return true
		}
		id, err := dto.ParseUUID(requested)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return false
		}
		query.UserID = &id
		return true
	}
	if isGlobalAPIKeyItemRequest(request, requested) {
		return true
	}
	user, ok := resolveItemUser(w, request, requested, h.users)
	if !ok {
		return false
	}
	applyItemUser(query, user)
	return true
}
