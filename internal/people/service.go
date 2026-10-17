// SPDX-License-Identifier: GPL-2.0-only

// Package people ports the Person metadata refresh: the on-demand refresh of
// UserLibraryController.GetItem and the "Refresh People" scheduled task
// (PeopleValidationTask). It loads the stored person, runs the remote person
// provider, and persists the result through the item store's edit write.
package people

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// onDemandInterval is the minimum age of DateLastRefreshed before GetItem
// refreshes a person without metadata again
// (UserLibraryController.RefreshItemOnDemandIfNeeded).
const onDemandInterval = 3 * 24 * time.Hour

// Items is the person persistence seam. *store.ItemStore satisfies it.
type Items interface {
	GetBatch(context.Context, []dto.UUID) ([]item.Item, error)
	EditItems(ctx context.Context, edited []item.Item, entities []item.Item) error
	PeopleMissingMetadata(context.Context) ([]dto.UUID, error)
}

// Settings supplies the server metadata language and country.
// *config.Manager satisfies it.
type Settings interface {
	System() config.ServerConfiguration
}

// Options constructs a Service. Logger may be nil.
type Options struct {
	Items    Items
	Provider providers.PersonProvider
	Settings Settings
	Clock    clock.Clock
	Logger   *slog.Logger
}

// Service refreshes Person metadata. It holds no mutable state.
type Service struct {
	items    Items
	provider providers.PersonProvider
	settings Settings
	clock    clock.Clock
	logger   *slog.Logger
}

// NewService builds a Service. Items, Provider, Settings, and Clock are
// required.
func NewService(options Options) (*Service, error) {
	if options.Items == nil || options.Provider == nil || options.Settings == nil || options.Clock == nil {
		return nil, errors.New("people: items, provider, settings, and clock are required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{items: options.Items, provider: options.Provider, settings: options.Settings, clock: options.Clock, logger: logger}, nil
}

// RefreshOnDemand ports UserLibraryController.RefreshItemOnDemandIfNeeded.
// A Person without an overview or without a Primary image gets a full
// metadata and image refresh when it was last refreshed 3 or more days ago,
// or never. Upstream awaits the refresh before it builds the response. It
// returns the refreshed item and true, or value and false when it did not
// refresh.
func (s *Service) RefreshOnDemand(ctx context.Context, value item.Item) (item.Item, bool, error) {
	if value.Kind != item.KindPerson || hasMetadata(value) {
		return value, false, nil
	}
	if value.DateLastRefreshed != nil && s.clock.Now().Sub(*value.DateLastRefreshed) < onDemandInterval {
		return value, false, nil
	}
	refreshed, err := s.refresh(ctx, value.ID, func(item.Item) (providers.PersonRefresh, bool) {
		return providers.PersonRefresh{Metadata: true, Images: true}, true
	})
	if err != nil || refreshed == nil {
		return value, false, err
	}
	return *refreshed, true, nil
}

// Run ports the person refresh phase of PeopleValidationTask
// (RefreshPeopleImagesAsync): every person without a Primary image or an
// overview gets a Default-mode refresh. A Default-mode refresh calls the
// remote providers only on a first refresh, so the candidates are the people
// that were never refreshed. One failed person is logged and the task
// continues.
func (s *Service) Run(ctx context.Context, progress func(float64)) error {
	report := func(value float64) {
		if progress != nil {
			progress(value)
		}
	}
	report(0)
	ids, err := s.items.PeopleMissingMetadata(ctx)
	if err != nil {
		return fmt.Errorf("people: list people missing metadata: %w", err)
	}
	s.logger.LogAttrs(ctx, slog.LevelDebug, "people needing image/overview refresh", slog.Int("count", len(ids)))
	refreshed := 0
	for index, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := s.refresh(ctx, id, func(person item.Item) (providers.PersonRefresh, bool) {
			// PeopleValidationTask.RefreshPersonAsync: ValidationOnly for
			// the part the person already has, Default for the rest.
			request := providers.PersonRefresh{
				Metadata: !hasOverview(person),
				Images:   !hasPrimaryImage(person),
			}
			return request, person.DateLastRefreshed == nil && (request.Metadata || request.Images)
		})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			s.logger.LogAttrs(ctx, slog.LevelError, "error refreshing person", slog.String("person", id.String()), slog.String("error", err.Error()))
		case result != nil:
			refreshed++
		}
		report(100 * float64(index+1) / float64(len(ids)))
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "refreshed metadata for people missing images or overview", slog.Int("count", refreshed))
	report(100)
	return nil
}

// refresh loads the person, asks plan for the refresh request, runs the
// provider, and persists the result with DateLastRefreshed set, as
// MetadataService.RefreshMetadata saves a first or forced refresh. It
// returns nil when the person is gone or plan declines.
func (s *Service) refresh(ctx context.Context, id dto.UUID, plan func(item.Item) (providers.PersonRefresh, bool)) (*item.Item, error) {
	values, err := s.items.GetBatch(ctx, []dto.UUID{id})
	if err != nil {
		return nil, err
	}
	if len(values) == 0 || values[0].Kind != item.KindPerson {
		return nil, nil
	}
	person := values[0]
	request, ok := plan(person)
	if !ok {
		return nil, nil
	}
	system := s.settings.System()
	request.Language = strings.TrimSpace(system.PreferredMetadataLanguage)
	request.Country = strings.TrimSpace(system.MetadataCountryCode)
	if _, err := s.provider.RefreshPerson(ctx, request, &person); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// MetadataService logs a provider failure and does not set
		// DateLastRefreshed, so the next refresh tries the person again.
		s.logger.LogAttrs(ctx, slog.LevelWarn, "person provider failed", slog.String("person", id.String()), slog.String("error", err.Error()))
		return nil, nil
	}
	now := s.clock.Now().UTC()
	person.DateLastRefreshed = &now
	if err := s.items.EditItems(ctx, []item.Item{person}, nil); err != nil {
		return nil, err
	}
	return &person, nil
}

// hasMetadata is the RefreshItemOnDemandIfNeeded test: an overview and a
// Primary image.
func hasMetadata(value item.Item) bool {
	return hasOverview(value) && hasPrimaryImage(value)
}

func hasOverview(value item.Item) bool {
	return value.Overview != nil && strings.TrimSpace(*value.Overview) != ""
}

func hasPrimaryImage(value item.Item) bool {
	for _, image := range value.ImageInfos {
		if image.Type == item.ImageTypePrimary {
			return true
		}
	}
	return false
}
