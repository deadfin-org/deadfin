// SPDX-License-Identifier: GPL-2.0-only

package user

import (
	"context"
	"sort"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/store"
)

// Devices ports DeviceManager.GetDevicesForUser
// (Jellyfin.Server.Implementations/Devices/DeviceManager.cs:181-208). It
// returns every token row, newest activity first, then by device ID. A
// non-nil userID keeps only the devices that CanAccessDevice allows for that
// user (DeviceManager.cs:237-249): an administrator or EnableAllDevices allows
// all, else the device must be in EnabledDevices or report no persistent
// identifier. persistentIdentifier reports the device's
// SupportsPersistentIdentifier capability. An unknown user is
// store.ErrNotFound.
func (s *Service) Devices(ctx context.Context, userID dto.UUID, persistentIdentifier func(deviceID string) bool) ([]store.DeviceRecord, error) {
	records, err := s.identity.ListDeviceRecords(ctx, "")
	if err != nil {
		return nil, err
	}
	// LINQ OrderByDescending(DateLastActivity).ThenBy(DeviceId) is stable; the
	// id order of the rows breaks the remaining ties (D-04).
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].DateLastActivity.Equal(records[j].DateLastActivity) {
			return records[i].DateLastActivity.After(records[j].DateLastActivity)
		}
		return records[i].DeviceID < records[j].DeviceID
	})
	if userID == dto.NilUUID {
		return records, nil
	}
	account, err := s.identity.User(ctx, userID)
	if err != nil {
		return nil, err
	}
	policy, err := userPolicy(account)
	if err != nil {
		return nil, err
	}
	allowed := records[:0]
	for _, record := range records {
		if policy.allowsDevice(record.DeviceID) || (persistentIdentifier != nil && !persistentIdentifier(record.DeviceID)) {
			allowed = append(allowed, record)
		}
	}
	return allowed, nil
}

// Device ports DeviceManager.GetDevice (DeviceManager.cs:136-143): the token
// row of the device with the newest activity. A device without a row is
// store.ErrNotFound.
func (s *Service) Device(ctx context.Context, deviceID string) (store.DeviceRecord, error) {
	records, err := s.DeviceTokens(ctx, deviceID)
	if err != nil {
		return store.DeviceRecord{}, err
	}
	if len(records) == 0 {
		return store.DeviceRecord{}, store.ErrNotFound
	}
	newest := records[0]
	for _, record := range records[1:] {
		if record.DateLastActivity.After(newest.DateLastActivity) {
			newest = record
		}
	}
	return newest, nil
}

// DeviceTokens returns every token row of one device, in id order. An empty
// device ID has no rows.
func (s *Service) DeviceTokens(ctx context.Context, deviceID string) ([]store.DeviceRecord, error) {
	if deviceID == "" {
		return []store.DeviceRecord{}, nil
	}
	return s.identity.ListDeviceRecords(ctx, deviceID)
}

// DeviceOptions ports DeviceManager.GetDeviceOptions (DeviceManager.cs:
// 112-120). A device without saved options is store.ErrNotFound.
func (s *Service) DeviceOptions(ctx context.Context, deviceID string) (store.DeviceOptions, error) {
	return s.identity.GetDeviceOptions(ctx, deviceID)
}

// UpdateDeviceOptions ports DeviceManager.UpdateDeviceOptions
// (DeviceManager.cs:75-94): it creates or replaces the custom name. The
// device does not need a token row.
func (s *Service) UpdateDeviceOptions(ctx context.Context, deviceID string, customName *string) error {
	return s.identity.PutDeviceOptions(ctx, store.DeviceOptions{DeviceID: deviceID, CustomName: customName})
}
