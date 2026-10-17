// SPDX-License-Identifier: GPL-2.0-only

package providers

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/item"
)

// PersonRefresh is the refresh intent for one Person item. Upstream runs
// PersonMetadataService with the IRemoteMetadataProvider<Person> and
// IRemoteImageProvider implementations; a person has no library, so the
// server metadata language and country apply.
type PersonRefresh struct {
	// Language and Country are the metadata language and country code.
	Language string
	Country  string
	// Metadata runs the metadata provider (GetMetadata).
	Metadata bool
	// ReplaceAllMetadata replaces stored values instead of filling empty
	// ones (MergeBaseItemData replaceData).
	ReplaceAllMetadata bool
	// Images runs the image provider for missing image types.
	Images bool
}

// PersonProvider refreshes Person metadata and images from a remote source.
// It returns handled=false when it has nothing for the person.
type PersonProvider interface {
	RefreshPerson(ctx context.Context, request PersonRefresh, target *item.Item) (bool, error)
}
