// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"context"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/item"
)

// PeopleMissingMetadata returns the ids of the Person items that were never
// refreshed and have no Primary image or no overview, in id order. It is the
// candidate query of PeopleValidationTask.RefreshPeopleImagesAsync limited
// to the people a Default-mode refresh sends to the remote providers: upstream
// MetadataService runs them only on a first refresh, because a person has
// no library with an automatic refresh interval.
func (s *ItemStore) PeopleMissingMetadata(ctx context.Context) ([]dto.UUID, error) {
	rows, err := s.stores.Reader().QueryContext(ctx,
		"SELECT i.id FROM items i WHERE i.kind = "+s.ph(1)+
			" AND i.date_last_refreshed IS NULL"+
			" AND (i.overview IS NULL OR i.overview = ''"+
			" OR NOT EXISTS (SELECT 1 FROM item_images m WHERE m.item_id = i.id AND m.type = "+s.ph(2)+"))"+
			" ORDER BY i.id",
		item.KindPerson.String(), item.ImageTypePrimary.String())
	if err != nil {
		return nil, err
	}
	var ids []dto.UUID
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, closeRows(rows, err)
		}
		id, err := scanUUID(raw)
		if err != nil {
			return nil, closeRows(rows, err)
		}
		ids = append(ids, id)
	}
	return ids, closeRows(rows, rows.Err())
}
