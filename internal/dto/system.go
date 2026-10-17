// SPDX-License-Identifier: GPL-2.0-only

package dto

// PublicSystemInfo is the public server identity returned by
// GET /System/Info/Public. Its concrete fields match the stable upstream DTO.
type PublicSystemInfo struct {
	LocalAddress           string `json:"LocalAddress"`
	ServerName             string `json:"ServerName"`
	Version                string `json:"Version"`
	ProductName            string `json:"ProductName"`
	OperatingSystem        string `json:"OperatingSystem"`
	ID                     UUID   `json:"Id"`
	StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
}

// CastReceiverApplication identifies a Google Cast receiver application in a
// SystemInfo response.
type CastReceiverApplication struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// SystemInfo is the authenticated server identity returned by GET /System/Info.
// It extends discovery's public projection with the complete upstream
// MediaBrowser.Model.System.SystemInfo surface.
type SystemInfo struct {
	PublicSystemInfo
	OperatingSystemDisplayName string                    `json:"OperatingSystemDisplayName"`
	PackageName                *string                   `json:"PackageName,omitempty"`
	HasPendingRestart          bool                      `json:"HasPendingRestart"`
	IsShuttingDown             bool                      `json:"IsShuttingDown"`
	SupportsLibraryMonitor     bool                      `json:"SupportsLibraryMonitor"`
	WebSocketPortNumber        int                       `json:"WebSocketPortNumber"`
	CompletedInstallations     []any                     `json:"CompletedInstallations"`
	CanSelfRestart             bool                      `json:"CanSelfRestart"`
	CanSelfUpdate              bool                      `json:"CanSelfUpdate"`
	CanLaunchWebBrowser        bool                      `json:"CanLaunchWebBrowser"`
	ProgramDataPath            *string                   `json:"ProgramDataPath"`
	WebPath                    *string                   `json:"WebPath"`
	ItemsByNamePath            *string                   `json:"ItemsByNamePath"`
	CachePath                  *string                   `json:"CachePath"`
	LogPath                    *string                   `json:"LogPath"`
	InternalMetadataPath       *string                   `json:"InternalMetadataPath"`
	TranscodingTempPath        *string                   `json:"TranscodingTempPath"`
	CastReceiverApplications   []CastReceiverApplication `json:"CastReceiverApplications"`
	HasUpdateAvailable         bool                      `json:"HasUpdateAvailable"`
	EncoderLocation            string                    `json:"EncoderLocation"`
	SystemArchitecture         string                    `json:"SystemArchitecture"`
}

// ClientLogDocumentResponseDto is the POST /ClientLog/Document result. It
// matches Jellyfin.Api.Models.ClientLogDtos.ClientLogDocumentResponseDto.
type ClientLogDocumentResponseDto struct {
	FileName string `json:"FileName"`
}
