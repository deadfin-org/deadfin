// SPDX-License-Identifier: GPL-2.0-only

// Package sessiondto projects the in-memory session registry into Jellyfin's
// shared HTTP and WebSocket SessionInfoDto representation.
package sessiondto

import (
	"context"
	"encoding/json"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/dtobuild"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
	"github.com/deadfin-org/deadfin/internal/user"
)

// Projector owns the store-backed portions of a SessionInfoDto projection.
// Both GET /Sessions and the Sessions WebSocket feed use this instance so the
// two transports cannot drift.
type Projector struct {
	users       *user.Service
	items       *store.ItemStore
	builder     *dtobuild.Builder
	serverID    string
	transcoding TranscodingSource
}

// TranscodingSource reports the running transcode of a device. Upstream
// keeps it on SessionInfo.TranscodingInfo (SessionManager.ReportTranscodingInfo)
// while the transcoding job runs.
type TranscodingSource interface {
	TranscodingInfo(deviceID string) (dto.TranscodingInfo, bool)
}

// New constructs a shared session projector. Nil store dependencies retain the
// registry-only projection used by route-ledger and focused handler tests.
func New(users *user.Service, items *store.ItemStore, builder *dtobuild.Builder, serverID string) *Projector {
	return &Projector{users: users, items: items, builder: builder, serverID: serverID}
}

// WithTranscoding returns a copy of the projector that fills
// TranscodingInfo from source. A nil source leaves it empty.
func (projector *Projector) WithTranscoding(source TranscodingSource) *Projector {
	copied := *projector
	copied.transcoding = source
	return &copied
}

// Project renders one immutable registry snapshot.
func (projector *Projector) Project(ctx context.Context, snapshot session.Snapshot, userName string) dto.SessionInfoDto {
	projected := ProjectBase(snapshot, userName, projector.serverID)
	if projector.transcoding != nil {
		if info, ok := projector.transcoding.TranscodingInfo(snapshot.DeviceID); ok {
			projected.TranscodingInfo = &info
		}
	}
	if projector.users != nil && len(snapshot.AdditionalUserIDs) != 0 {
		projected.AdditionalUsers = make([]dto.SessionUserInfo, len(snapshot.AdditionalUserIDs))
		for index, userID := range snapshot.AdditionalUserIDs {
			additionalUser := dto.SessionUserInfo{UserID: userID}
			if value, err := projector.users.GetDTO(ctx, userID); err == nil {
				additionalUser.UserName = value.Name
			}
			projected.AdditionalUsers[index] = additionalUser
		}
	}
	if snapshot.NowPlaying == nil || projector.items == nil || projector.builder == nil {
		return projected
	}
	value, err := projector.items.Get(ctx, snapshot.NowPlaying.ItemID)
	if err != nil {
		return projected
	}
	items, err := projector.builder.Build(ctx, []item.Item{value}, store.ItemQueryDTOOptions{}, &snapshot.UserID)
	if err != nil || len(items.Items) != 1 {
		return projected
	}
	encoded, err := json.Marshal(items.Items[0])
	if err != nil {
		return projected
	}
	nowPlayingItem := json.RawMessage(encoded)
	projected.NowPlayingItem = &nowPlayingItem
	return projected
}

// ProjectBase renders fields that are wholly owned by the session registry.
func ProjectBase(snapshot session.Snapshot, userName, serverID string) dto.SessionInfoDto {
	id := snapshot.ID
	client := snapshot.Client
	device := snapshot.DeviceID
	name := snapshot.DeviceName
	version := snapshot.Version
	var remote *string
	if snapshot.RemoteEndPoint != "" {
		remote = &snapshot.RemoteEndPoint
	}
	playState := dto.PlayerStateInfo{CanSeek: false, IsMuted: false, IsPaused: false, PlaybackOrder: "Default", RepeatMode: "RepeatNone"}
	if snapshot.NowPlaying != nil {
		playState = dto.PlayerStateInfo{
			PositionTicks:       snapshot.NowPlaying.PositionTicks,
			CanSeek:             snapshot.NowPlaying.CanSeek,
			IsMuted:             snapshot.NowPlaying.IsMuted,
			IsPaused:            snapshot.NowPlaying.IsPaused,
			VolumeLevel:         snapshot.NowPlaying.VolumeLevel,
			AudioStreamIndex:    snapshot.NowPlaying.AudioStreamIndex,
			SubtitleStreamIndex: snapshot.NowPlaying.SubtitleStreamIndex,
			MediaSourceID:       snapshot.NowPlaying.MediaSourceID,
			PlayMethod:          snapshot.NowPlaying.PlayMethod,
			RepeatMode:          snapshot.NowPlaying.RepeatMode,
			PlaybackOrder:       snapshot.NowPlaying.PlaybackOrder,
			LiveStreamID:        snapshot.NowPlaying.LiveStreamID,
		}
	}
	media := append([]string{}, snapshot.Capabilities.PlayableMediaTypes...)
	commands := append([]string{}, snapshot.Capabilities.SupportedCommands...)
	if media == nil {
		media = []string{}
	}
	if commands == nil {
		commands = []string{}
	}
	queue := append([]dto.QueueItem(nil), snapshot.NowPlayingQueue...)
	if queue == nil {
		queue = []dto.QueueItem{}
	}
	var pausedAt *dto.Time
	if snapshot.LastPausedDate != nil {
		value := dto.NewTime(*snapshot.LastPausedDate)
		pausedAt = &value
	}
	var playlistItemID *string
	if snapshot.PlaylistItemID != "" {
		playlistItemID = &snapshot.PlaylistItemID
	}
	capabilities := dto.ClientCapabilities{
		PlayableMediaTypes:           dto.CommaDelimitedStrings(media),
		SupportedCommands:            dto.CommaDelimitedStrings(commands),
		SupportsMediaControl:         snapshot.Capabilities.SupportsMediaControl,
		SupportsPersistentIdentifier: snapshot.Capabilities.SupportsPersistentIdentifier,
	}
	return dto.SessionInfoDto{
		PlayState:             &playState,
		AdditionalUsers:       []dto.SessionUserInfo{},
		Capabilities:          &capabilities,
		RemoteEndPoint:        remote,
		PlayableMediaTypes:    media,
		ID:                    &id,
		UserID:                snapshot.UserID,
		UserName:              &userName,
		Client:                &client,
		LastActivityDate:      dto.NewTime(snapshot.LastActivity),
		LastPlaybackCheckIn:   dto.NewTime(snapshot.LastPlaybackCheckIn),
		LastPausedDate:        pausedAt,
		DeviceName:            &name,
		DeviceID:              &device,
		ApplicationVersion:    &version,
		IsActive:              true,
		SupportsMediaControl:  false,
		SupportsRemoteControl: snapshot.SupportsRemoteControl,
		NowPlayingQueue:       queue,
		PlaylistItemID:        playlistItemID,
		ServerID:              &serverID,
		SupportedCommands:     commands,
	}
}
