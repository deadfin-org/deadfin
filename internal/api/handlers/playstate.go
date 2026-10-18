// SPDX-License-Identifier: GPL-2.0-only

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/deadfin-org/deadfin/internal/api/bind"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
)

// RegisterPlaystate registers the modern playback reporting endpoints and the
// legacy aliases retained for older Jellyfin clients. Production supplies the
// active-session registry so every report is bound to its current target.
func RegisterPlaystate(rt *router.Router, service playstate.Application, activeSessions ...*session.Manager) {
	var sessions *session.Manager
	if len(activeSessions) != 0 {
		sessions = activeSessions[0]
	}
	RegisterPlaystateWithGrants(rt, service, sessions, nil, nil)
}

// TranscodingJobs reports whether a transcoding job runs for a
// PlaySessionId (upstream ITranscodeManager.GetTranscodingJob).
type TranscodingJobs interface {
	HasTranscodingJob(playSessionID string) bool
}

// PlaySessionToucher refreshes the idle timer of a PlaySessionId grant (D-54).
type PlaySessionToucher interface {
	Touch(playSessionID string, userID dto.UUID)
}

// RegisterPlaystateWithGrants is RegisterPlaystate with a grant store: every
// playback report and ping keeps the reporting user's PlaySessionId grant
// alive, so a long pause does not expire a direct stream (D-54). jobs
// decides whether a Transcode report stays Transcode; nil means no job runs.
func RegisterPlaystateWithGrants(rt *router.Router, service playstate.Application, sessions *session.Manager, grants PlaySessionToucher, jobs TranscodingJobs) {
	h := playstateHandler{service: service, sessions: sessions, grants: grants, jobs: jobs}
	policy := string(auth.PolicyDefaultAuthorization)

	for _, route := range []struct {
		method, pattern, operationID string
		handler                      http.HandlerFunc
	}{
		{http.MethodPost, "/Sessions/Playing", "ReportPlaybackStart", h.start},
		{http.MethodPost, "/Sessions/Playing/Progress", "ReportPlaybackProgress", h.progress},
		{http.MethodPost, "/Sessions/Playing/Stopped", "ReportPlaybackStopped", h.stop},
		{http.MethodPost, "/Sessions/Playing/Ping", "PingPlaybackSession", h.ping},
		{http.MethodPost, "/PlayingItems/{itemId}", "SHADOW:OnPlaybackStart", h.legacyStart},
		{http.MethodPost, "/Users/{userId}/PlayingItems/{itemId}", "SHADOW:OnPlaybackStartLegacy", h.legacyStart},
		{http.MethodPost, "/PlayingItems/{itemId}/Progress", "SHADOW:OnPlaybackProgress", h.legacyProgress},
		{http.MethodPost, "/Users/{userId}/PlayingItems/{itemId}/Progress", "SHADOW:OnPlaybackProgressLegacy", h.legacyProgress},
		{http.MethodDelete, "/PlayingItems/{itemId}", "SHADOW:OnPlaybackStopped", h.legacyStop},
		{http.MethodDelete, "/Users/{userId}/PlayingItems/{itemId}", "SHADOW:OnPlaybackStoppedLegacy", h.legacyStop},
	} {
		rt.Handle(route.method, route.pattern, router.Route{OperationID: route.operationID, Policy: policy}, route.handler)
	}
}

type playstateHandler struct {
	service  playstate.Application
	sessions *session.Manager
	grants   PlaySessionToucher
	jobs     TranscodingJobs
}

func (h playstateHandler) touchGrant(r *http.Request, playSessionID string) {
	if h.grants == nil || playSessionID == "" {
		return
	}
	if identity, ok := middleware.IdentityFromContext(r.Context()); ok && identity.Authenticated {
		h.grants.Touch(playSessionID, identity.UserID)
	}
}

// playstateReportDTO is intentionally a transport-only projection. SessionId
// and Item are bound only to preserve upstream model-validation behavior; they
// never choose the target session or item.
type playstateReportDTO struct {
	CanSeek                *bool             `json:"CanSeek" query:"canSeek"`
	ItemID                 *dto.UUID         `json:"ItemId" query:"itemId"`
	MediaSourceID          *string           `json:"MediaSourceId" query:"mediaSourceId"`
	AudioStreamIndex       *int              `json:"AudioStreamIndex" query:"audioStreamIndex"`
	SubtitleStreamIndex    *int              `json:"SubtitleStreamIndex" query:"subtitleStreamIndex"`
	IsPaused               *bool             `json:"IsPaused" query:"isPaused"`
	IsMuted                *bool             `json:"IsMuted" query:"isMuted"`
	PositionTicks          *int64            `json:"PositionTicks" query:"positionTicks"`
	PlaybackStartTimeTicks *int64            `json:"PlaybackStartTimeTicks" query:"playbackStartTimeTicks"`
	VolumeLevel            *int              `json:"VolumeLevel" query:"volumeLevel"`
	Brightness             *int              `json:"Brightness" query:"brightness"`
	AspectRatio            *string           `json:"AspectRatio" query:"aspectRatio"`
	PlayMethod             *playstateMethod  `json:"PlayMethod" query:"playMethod"`
	LiveStreamID           *string           `json:"LiveStreamId" query:"liveStreamId"`
	PlaySessionID          *string           `json:"PlaySessionId" query:"playSessionId"`
	RepeatMode             *playstateRepeat  `json:"RepeatMode" query:"repeatMode"`
	PlaybackOrder          *playstateOrder   `json:"PlaybackOrder" query:"playbackOrder"`
	PlaylistItemID         *string           `json:"PlaylistItemId" query:"playlistItemId"`
	NowPlayingQueue        *[]dto.QueueItem  `json:"NowPlayingQueue"`
	SessionID              *string           `json:"SessionId" query:"-"`
	Item                   *playstateItemDTO `json:"Item" query:"-"`
}

// playstateItemDTO models only the validated portion of an ignored upstream
// Item. A pointer retains the upstream distinction between an absent/null Item
// and an object, while the typed ID rejects non-string GUID values.
type playstateItemDTO struct {
	ID           *dto.UUID `json:"Id"`
	RunTimeTicks *int64    `json:"RunTimeTicks"`
}

type legacyStartPlaystateDTO struct {
	CanSeek             *bool            `query:"canSeek"`
	MediaSourceID       *string          `query:"mediaSourceId"`
	AudioStreamIndex    *int             `query:"audioStreamIndex"`
	SubtitleStreamIndex *int             `query:"subtitleStreamIndex"`
	PlayMethod          *playstateMethod `query:"playMethod"`
	LiveStreamID        *string          `query:"liveStreamId"`
	PlaySessionID       *string          `query:"playSessionId"`
}

func (dto legacyStartPlaystateDTO) report() playstate.Report {
	return playstateReportDTO{
		CanSeek:             dto.CanSeek,
		MediaSourceID:       dto.MediaSourceID,
		AudioStreamIndex:    dto.AudioStreamIndex,
		SubtitleStreamIndex: dto.SubtitleStreamIndex,
		PlayMethod:          dto.PlayMethod,
		LiveStreamID:        dto.LiveStreamID,
		PlaySessionID:       dto.PlaySessionID,
	}.report(true)
}

type legacyProgressPlaystateDTO struct {
	MediaSourceID       *string          `query:"mediaSourceId"`
	AudioStreamIndex    *int             `query:"audioStreamIndex"`
	SubtitleStreamIndex *int             `query:"subtitleStreamIndex"`
	IsPaused            *bool            `query:"isPaused"`
	IsMuted             *bool            `query:"isMuted"`
	PositionTicks       *int64           `query:"positionTicks"`
	VolumeLevel         *int             `query:"volumeLevel"`
	PlayMethod          *playstateMethod `query:"playMethod"`
	LiveStreamID        *string          `query:"liveStreamId"`
	PlaySessionID       *string          `query:"playSessionId"`
	RepeatMode          *playstateRepeat `query:"repeatMode"`
}

func (dto legacyProgressPlaystateDTO) report() playstate.Report {
	return playstateReportDTO{
		MediaSourceID:       dto.MediaSourceID,
		AudioStreamIndex:    dto.AudioStreamIndex,
		SubtitleStreamIndex: dto.SubtitleStreamIndex,
		IsPaused:            dto.IsPaused,
		IsMuted:             dto.IsMuted,
		PositionTicks:       dto.PositionTicks,
		VolumeLevel:         dto.VolumeLevel,
		PlayMethod:          dto.PlayMethod,
		LiveStreamID:        dto.LiveStreamID,
		PlaySessionID:       dto.PlaySessionID,
		RepeatMode:          dto.RepeatMode,
	}.report(true)
}

type playstateStopDTO struct {
	ItemID          *dto.UUID         `json:"ItemId" query:"itemId"`
	MediaSourceID   *string           `json:"MediaSourceId" query:"mediaSourceId"`
	PositionTicks   *int64            `json:"PositionTicks" query:"positionTicks"`
	LiveStreamID    *string           `json:"LiveStreamId" query:"liveStreamId"`
	PlaySessionID   *string           `json:"PlaySessionId" query:"playSessionId"`
	PlaylistItemID  *string           `json:"PlaylistItemId" query:"playlistItemId"`
	NowPlayingQueue *[]dto.QueueItem  `json:"NowPlayingQueue"`
	Failed          *bool             `json:"Failed" query:"failed"`
	SessionID       *string           `json:"SessionId" query:"-"`
	Item            *playstateItemDTO `json:"Item" query:"-"`
	NextMediaType   *string           `json:"NextMediaType" query:"-"`
}

func (dto playstateStopDTO) report() playstate.Report {
	report := playstateReportDTO{
		ItemID:          dto.ItemID,
		MediaSourceID:   dto.MediaSourceID,
		PositionTicks:   dto.PositionTicks,
		LiveStreamID:    dto.LiveStreamID,
		PlaySessionID:   dto.PlaySessionID,
		PlaylistItemID:  dto.PlaylistItemID,
		NowPlayingQueue: dto.NowPlayingQueue,
	}.report(false)
	if dto.Failed != nil {
		report.Failed = *dto.Failed
	}
	return report
}

func (h playstateHandler) start(w http.ResponseWriter, r *http.Request) {
	report, ok := modernPlaystateReport(w, r, true)
	if !ok {
		return
	}
	h.report(w, r, playstateStart, report)
}

func (h playstateHandler) progress(w http.ResponseWriter, r *http.Request) {
	report, ok := modernPlaystateReport(w, r, true)
	if !ok {
		return
	}
	h.report(w, r, playstateProgress, report)
}

func (h playstateHandler) stop(w http.ResponseWriter, r *http.Request) {
	report, ok := modernStopPlaystateReport(w, r)
	if !ok || !validStopReport(w, report) {
		return
	}
	h.report(w, r, playstateStop, report)
}

func (h playstateHandler) ping(w http.ResponseWriter, r *http.Request) {
	var query struct {
		PlaySessionID *string `query:"playSessionId"`
	}
	if err := bind.QueryRequest(r, &query); err != nil || query.PlaySessionID == nil || *query.PlaySessionID == "" {
		playstateBadRequest(w)
		return
	}
	h.touchGrant(r, *query.PlaySessionID)
	w.WriteHeader(http.StatusNoContent)
}

func (h playstateHandler) legacyStart(w http.ResponseWriter, r *http.Request) {
	h.legacyReport(w, r, playstateStart)
}

func (h playstateHandler) legacyProgress(w http.ResponseWriter, r *http.Request) {
	h.legacyReport(w, r, playstateProgress)
}

func (h playstateHandler) legacyStop(w http.ResponseWriter, r *http.Request) {
	h.legacyReport(w, r, playstateStop)
}

func (h playstateHandler) legacyReport(w http.ResponseWriter, r *http.Request, operation playstateOperation) {
	if chi.URLParam(r, "userId") != "" {
		if _, ok := playstateRouteUUID(w, r, "userId"); !ok {
			return
		}
	}
	itemID, ok := playstateRouteUUID(w, r, "itemId")
	if !ok {
		return
	}

	var report playstate.Report
	switch operation {
	case playstateStart:
		var query legacyStartPlaystateDTO
		if err := bind.QueryRequest(r, &query); err != nil {
			playstateBadRequest(w)
			return
		}
		report = query.report()
	case playstateProgress:
		var query legacyProgressPlaystateDTO
		if err := bind.QueryRequest(r, &query); err != nil {
			playstateBadRequest(w)
			return
		}
		report = query.report()
	case playstateStop:
		var query playstateStopDTO
		if err := bind.QueryRequest(r, &query); err != nil {
			playstateBadRequest(w)
			return
		}
		report = query.report()
	}
	report.ItemID = itemID
	report = normalizeMediaSourceID(report)
	if operation == playstateStop && !validStopReport(w, report) {
		return
	}
	h.report(w, r, operation, report)
}

func modernPlaystateReport(w http.ResponseWriter, r *http.Request, includePlaybackEnums bool) (playstate.Report, bool) {
	var body playstateReportDTO
	if err := bind.JSONBody(r, &body); err != nil {
		playstateBadRequest(w)
		return playstate.Report{}, false
	}
	return normalizeMediaSourceID(body.report(includePlaybackEnums)), true
}

func modernStopPlaystateReport(w http.ResponseWriter, r *http.Request) (playstate.Report, bool) {
	var body playstateStopDTO
	if err := bind.JSONBody(r, &body); err != nil {
		playstateBadRequest(w)
		return playstate.Report{}, false
	}
	return normalizeMediaSourceID(body.report()), true
}

func (dto playstateReportDTO) report(includePlaybackEnums bool) playstate.Report {
	state := session.PlaybackState{
		AudioStreamIndex:       dto.AudioStreamIndex,
		SubtitleStreamIndex:    dto.SubtitleStreamIndex,
		PositionTicks:          dto.PositionTicks,
		PlaybackStartTimeTicks: dto.PlaybackStartTimeTicks,
		VolumeLevel:            dto.VolumeLevel,
		NowPlayingQueue:        dto.NowPlayingQueue,
		Brightness:             dto.Brightness,
	}
	if dto.CanSeek != nil {
		state.CanSeek = *dto.CanSeek
	}
	if dto.ItemID != nil {
		state.ItemID = *dto.ItemID
	}
	if dto.Item != nil {
		state.RunTimeTicks = dto.Item.RunTimeTicks
	}
	if dto.MediaSourceID != nil {
		state.MediaSourceID = *dto.MediaSourceID
	}
	if dto.IsPaused != nil {
		state.IsPaused = *dto.IsPaused
	}
	if dto.IsMuted != nil {
		state.IsMuted = *dto.IsMuted
	}
	if dto.AspectRatio != nil {
		state.AspectRatio = *dto.AspectRatio
	}
	if dto.LiveStreamID != nil {
		state.LiveStreamID = *dto.LiveStreamID
	}
	if dto.PlaySessionID != nil {
		state.PlaySessionID = *dto.PlaySessionID
	}
	if includePlaybackEnums {
		if dto.RepeatMode != nil {
			state.RepeatMode = string(*dto.RepeatMode)
		} else {
			state.RepeatMode = "RepeatNone"
		}
		if dto.PlaybackOrder != nil {
			state.PlaybackOrder = string(*dto.PlaybackOrder)
		} else {
			state.PlaybackOrder = "Default"
		}
		// A missing PlayMethod binds to PlayMethod.Transcode, the enum's
		// zero value (PlaybackProgressInfo.PlayMethod; the legacy routes
		// use playMethod ?? PlayMethod.Transcode).
		if dto.PlayMethod != nil {
			state.PlayMethod = string(*dto.PlayMethod)
		} else {
			state.PlayMethod = "Transcode"
		}
	}
	if dto.PlaylistItemID != nil {
		state.PlaylistItemID = *dto.PlaylistItemID
	}

	return playstate.Report{PlaybackState: state}
}

func normalizeMediaSourceID(report playstate.Report) playstate.Report {
	if report.PlaybackState.MediaSourceID == "" && !report.PlaybackState.ItemID.IsNil() {
		report.PlaybackState.MediaSourceID = report.PlaybackState.ItemID.String()
	}
	return report
}

type playstateMethod string

func (method *playstateMethod) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	case "transcode":
		*method = "Transcode"
	case "directplay":
		*method = "DirectPlay"
	case "directstream":
		*method = "DirectStream"
	default:
		return fmt.Errorf("invalid play method %q", text)
	}
	return nil
}

func (method *playstateMethod) UnmarshalJSON(data []byte) error {
	return unmarshalPlaystateEnum(data, method.UnmarshalText)
}

type playstateRepeat string

func (repeat *playstateRepeat) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	case "repeatnone":
		*repeat = "RepeatNone"
	case "repeatall":
		*repeat = "RepeatAll"
	case "repeatone":
		*repeat = "RepeatOne"
	default:
		return fmt.Errorf("invalid repeat mode %q", text)
	}
	return nil
}

func (repeat *playstateRepeat) UnmarshalJSON(data []byte) error {
	return unmarshalPlaystateEnum(data, repeat.UnmarshalText)
}

type playstateOrder string

func (order *playstateOrder) UnmarshalText(text []byte) error {
	switch strings.ToLower(string(text)) {
	case "default":
		*order = "Default"
	case "shuffle":
		*order = "Shuffle"
	default:
		return fmt.Errorf("invalid playback order %q", text)
	}
	return nil
}

func (order *playstateOrder) UnmarshalJSON(data []byte) error {
	return unmarshalPlaystateEnum(data, order.UnmarshalText)
}

func unmarshalPlaystateEnum(data []byte, unmarshal func([]byte) error) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return unmarshal([]byte(value))
}

// validatePlayMethod is PlaystateController.ValidatePlayMethod: Transcode
// stays Transcode only while a transcoding job runs for the PlaySessionId;
// otherwise the report is DirectPlay. Other methods pass through.
func (h playstateHandler) validatePlayMethod(method, playSessionID string) string {
	if method != "Transcode" {
		return method
	}
	if h.jobs == nil || strings.TrimSpace(playSessionID) == "" || !h.jobs.HasTranscodingJob(playSessionID) {
		return "DirectPlay"
	}
	return method
}

func validStopReport(w http.ResponseWriter, report playstate.Report) bool {
	if report.PositionTicks != nil && *report.PositionTicks < 0 {
		playstateBadRequest(w)
		return false
	}
	return true
}

func playstateBadRequest(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte("Error processing request."))
}

func playstateRouteUUID(w http.ResponseWriter, r *http.Request, name string) (dto.UUID, bool) {
	discard := playstateRouteErrorWriter{ResponseWriter: w, header: make(http.Header)}
	id, ok := routeUUID(&discard, r, name)
	if !ok {
		playstateBadRequest(w)
		return dto.NilUUID, false
	}
	return id, true
}

type playstateRouteErrorWriter struct {
	http.ResponseWriter
	header http.Header
}

func (w *playstateRouteErrorWriter) Header() http.Header {
	return w.header
}

func (*playstateRouteErrorWriter) WriteHeader(int) {}

func (*playstateRouteErrorWriter) Write(value []byte) (int, error) {
	return len(value), nil
}

type playstateOperation uint8

const (
	playstateStart playstateOperation = iota
	playstateProgress
	playstateStop
)

func (h playstateHandler) report(w http.ResponseWriter, r *http.Request, operation playstateOperation, report playstate.Report) {
	if h.service == nil {
		internalError(w)
		return
	}
	h.touchGrant(r, report.PlaySessionID)
	identity, _ := middleware.IdentityFromContext(r.Context())
	actor := playstate.Actor{UserID: identity.UserID, Client: identity.Client, DeviceID: identity.DeviceID}
	if h.sessions != nil {
		if snapshot, found := h.sessions.Session(identity.Client, identity.DeviceID); found {
			actor.Target = snapshot.Target
		}
	}
	if operation != playstateStop {
		report.PlayMethod = h.validatePlayMethod(report.PlayMethod, report.PlaySessionID)
	}
	var err error
	switch operation {
	case playstateStart:
		err = h.service.Start(r.Context(), actor, report)
	case playstateProgress:
		err = h.service.Progress(r.Context(), actor, report)
	case playstateStop:
		err = h.service.Stop(r.Context(), actor, report)
	}
	if err == nil || operation == playstateProgress && errors.Is(err, session.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if errors.Is(err, session.ErrNotFound) {
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	internalError(w)
}
