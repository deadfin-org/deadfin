// SPDX-License-Identifier: GPL-2.0-only

package config

import (
	"context"
	"encoding/xml"
	"fmt"
)

// XbmcMetadataOptions is the persisted NFO metadata configuration. It matches
// upstream/jellyfin/MediaBrowser.Model/Configuration/XbmcMetadataOptions.cs:
// 5-28. The constructor sets ReleaseDateFormat to "yyyy-MM-dd" and
// SaveImagePathsInNfo and EnablePathSubstitution to true. UserId is a nullable
// string and is omitted when null.
type XbmcMetadataOptions struct {
	UserId                       *string `json:"UserId,omitzero" xml:"UserId,omitempty"`
	ReleaseDateFormat            string
	SaveImagePathsInNfo          bool
	EnablePathSubstitution       bool
	EnableExtraThumbsDuplication bool
}

// DefaultXbmcMetadataOptions returns the upstream constructor defaults
// (XbmcMetadataOptions.cs:9-15).
func DefaultXbmcMetadataOptions() XbmcMetadataOptions {
	return XbmcMetadataOptions{
		ReleaseDateFormat:      "yyyy-MM-dd",
		SaveImagePathsInNfo:    true,
		EnablePathSubstitution: true,
	}
}

// MarshalXML writes the document root that upstream's XmlSerializer uses for
// xbmcmetadata.xml.
func (options XbmcMetadataOptions) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	start.Name.Local = "XbmcMetadataOptions"
	type fields XbmcMetadataOptions
	return encoder.EncodeElement(fields(options), start)
}

// UnmarshalXML accepts only the XbmcMetadataOptions document root. Fields
// that the source omits keep the defaults that the caller supplies.
func (options *XbmcMetadataOptions) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "XbmcMetadataOptions" {
		return fmt.Errorf("config: unexpected xbmcmetadata configuration root %q", start.Name.Local)
	}
	type fields XbmcMetadataOptions
	return decoder.DecodeElement((*fields)(options), &start)
}

// UnmarshalJSON decodes a posted configuration over the upstream defaults.
func (options *XbmcMetadataOptions) UnmarshalJSON(data []byte) error {
	type fields XbmcMetadataOptions
	decoded := fields(DefaultXbmcMetadataOptions())
	if err := decodeJSONOverDefaults(data, &decoded); err != nil {
		return err
	}
	*options = XbmcMetadataOptions(decoded)
	return nil
}

// XbmcMetadata returns an immutable value snapshot of the NFO metadata
// configuration.
func (manager *Manager) XbmcMetadata() XbmcMetadataOptions {
	manager.xbmcMetadata.mu.RLock()
	defer manager.xbmcMetadata.mu.RUnlock()
	options := manager.xbmcMetadata.value
	if options.UserId != nil {
		userID := *options.UserId
		options.UserId = &userID
	}
	return options
}

// UpdateXbmcMetadata serializes a durable mutation of xbmcmetadata.xml.
func (manager *Manager) UpdateXbmcMetadata(ctx context.Context, update func(*XbmcMetadataOptions) error) error {
	return updateConfiguration(ctx, manager, &manager.xbmcMetadata, XbmcMetadataFile, KeyXbmcMetadata, update)
}
