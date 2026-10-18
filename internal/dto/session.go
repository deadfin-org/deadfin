// SPDX-License-Identifier: GPL-2.0-only

package dto

import (
	"bytes"
	"encoding/json"
	"strings"
)

// SessionInfoDto is Jellyfin's public session representation.
type SessionInfoDto struct {
	PlayState             *PlayerStateInfo    `json:"PlayState,omitempty"`
	AdditionalUsers       []SessionUserInfo   `json:"AdditionalUsers"`
	Capabilities          *ClientCapabilities `json:"Capabilities,omitempty"`
	RemoteEndPoint        *string             `json:"RemoteEndPoint,omitempty"`
	PlayableMediaTypes    []string            `json:"PlayableMediaTypes"`
	ID                    *string             `json:"Id,omitempty"`
	UserID                UUID                `json:"UserId"`
	UserName              *string             `json:"UserName,omitempty"`
	Client                *string             `json:"Client,omitempty"`
	LastActivityDate      Time                `json:"LastActivityDate"`
	LastPlaybackCheckIn   Time                `json:"LastPlaybackCheckIn"`
	LastPausedDate        *Time               `json:"LastPausedDate,omitempty"`
	DeviceName            *string             `json:"DeviceName,omitempty"`
	DeviceType            *string             `json:"DeviceType,omitempty"`
	NowPlayingItem        *json.RawMessage    `json:"NowPlayingItem,omitempty"`
	NowViewingItem        *json.RawMessage    `json:"NowViewingItem,omitempty"`
	DeviceID              *string             `json:"DeviceId,omitempty"`
	ApplicationVersion    *string             `json:"ApplicationVersion,omitempty"`
	TranscodingInfo       *TranscodingInfo    `json:"TranscodingInfo,omitempty"`
	IsActive              bool                `json:"IsActive"`
	SupportsMediaControl  bool                `json:"SupportsMediaControl"`
	SupportsRemoteControl bool                `json:"SupportsRemoteControl"`
	NowPlayingQueue       []QueueItem         `json:"NowPlayingQueue"`
	HasCustomDeviceName   bool                `json:"HasCustomDeviceName"`
	PlaylistItemID        *string             `json:"PlaylistItemId,omitempty"`
	ServerID              *string             `json:"ServerId,omitempty"`
	UserPrimaryImageTag   *string             `json:"UserPrimaryImageTag,omitempty"`
	SupportedCommands     []string            `json:"SupportedCommands"`
}

// TranscodingInfo is Jellyfin's projection of a running transcode
// (MediaBrowser.Model/Session/TranscodingInfo.cs). TranscodeReasons is a
// non-nullable flags value, so it is always emitted.
type TranscodingInfo struct {
	AudioCodec               *string  `json:"AudioCodec,omitempty"`
	VideoCodec               *string  `json:"VideoCodec,omitempty"`
	Container                *string  `json:"Container,omitempty"`
	IsVideoDirect            bool     `json:"IsVideoDirect"`
	IsAudioDirect            bool     `json:"IsAudioDirect"`
	Bitrate                  *int     `json:"Bitrate,omitempty"`
	Framerate                *float32 `json:"Framerate,omitempty"`
	CompletionPercentage     *float64 `json:"CompletionPercentage,omitempty"`
	Width                    *int     `json:"Width,omitempty"`
	Height                   *int     `json:"Height,omitempty"`
	AudioChannels            *int     `json:"AudioChannels,omitempty"`
	HardwareAccelerationType *string  `json:"HardwareAccelerationType,omitempty"`
	TranscodeReasons         []string `json:"TranscodeReasons"`
}

// PlayerStateInfo is the current player-state projection carried by sessions.
// Keeping it typed, rather than embedding pre-encoded JSON, lets negotiated
// CamelCase rendering recurse through its public properties.
type PlayerStateInfo struct {
	PositionTicks       *int64 `json:"PositionTicks,omitempty"`
	CanSeek             bool   `json:"CanSeek"`
	IsPaused            bool   `json:"IsPaused"`
	IsMuted             bool   `json:"IsMuted"`
	VolumeLevel         *int   `json:"VolumeLevel,omitempty"`
	AudioStreamIndex    *int   `json:"AudioStreamIndex,omitempty"`
	SubtitleStreamIndex *int   `json:"SubtitleStreamIndex,omitempty"`
	MediaSourceID       string `json:"MediaSourceId,omitempty"`
	PlayMethod          string `json:"PlayMethod,omitempty"`
	RepeatMode          string `json:"RepeatMode"`
	PlaybackOrder       string `json:"PlaybackOrder"`
	LiveStreamID        string `json:"LiveStreamId,omitempty"`
}

// QueueItem identifies an item in a client-reported playback queue.
type QueueItem struct {
	ID             UUID    `json:"Id"`
	PlaylistItemID *string `json:"PlaylistItemId,omitempty"`
}

// SessionUserInfo identifies one additional user participating in a session.
type SessionUserInfo struct {
	UserID   UUID   `json:"UserId"`
	UserName string `json:"UserName"`
}

// ClientCapabilities describes a client's available session controls.
type ClientCapabilities struct {
	PlayableMediaTypes           CommaDelimitedStrings `json:"PlayableMediaTypes"`
	SupportedCommands            CommaDelimitedStrings `json:"SupportedCommands"`
	SupportsMediaControl         bool                  `json:"SupportsMediaControl"`
	SupportsPersistentIdentifier bool                  `json:"SupportsPersistentIdentifier"`
	DeviceProfile                *json.RawMessage      `json:"DeviceProfile,omitempty"`
	AppStoreURL                  *string               `json:"AppStoreUrl,omitempty"`
	IconURL                      *string               `json:"IconUrl,omitempty"`
}

// CommaDelimitedStrings reproduces Jellyfin's comma-delimited collection
// converter used for capability command and media-type lists.
type CommaDelimitedStrings []string

func (values *CommaDelimitedStrings) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*values = nil
		return nil
	}
	var joined string
	if err := json.Unmarshal(data, &joined); err == nil {
		parts := strings.Split(joined, ",")
		out := parts[:0]
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		*values = out
		return nil
	}
	var array []string
	if err := json.Unmarshal(data, &array); err != nil {
		return err
	}
	*values = array
	return nil
}
