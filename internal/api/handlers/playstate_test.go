// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/middleware"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/playstate"
	"github.com/deadfin-org/deadfin/internal/session"
	"github.com/deadfin-org/deadfin/internal/store"
)

// The legacy aliases are intentionally retained in the ledger, rather than
// hidden behind the modern operations: older Jellyfin clients still issue
// these paths and the ledger is the public transport contract.
func TestPlaystateRoutesPinModernAndLegacyLedger(t *testing.T) {
	rt := router.New()
	handlers.RegisterPlaystate(rt, nil)

	for _, want := range []router.Route{
		{Method: http.MethodPost, Pattern: "/Sessions/Playing", OperationID: "ReportPlaybackStart", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Sessions/Playing/Progress", OperationID: "ReportPlaybackProgress", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Sessions/Playing/Stopped", OperationID: "ReportPlaybackStopped", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Sessions/Playing/Ping", OperationID: "PingPlaybackSession", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/PlayingItems/{itemId}", OperationID: "SHADOW:OnPlaybackStart", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Users/{userId}/PlayingItems/{itemId}", OperationID: "SHADOW:OnPlaybackStartLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/PlayingItems/{itemId}/Progress", OperationID: "SHADOW:OnPlaybackProgress", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodPost, Pattern: "/Users/{userId}/PlayingItems/{itemId}/Progress", OperationID: "SHADOW:OnPlaybackProgressLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodDelete, Pattern: "/PlayingItems/{itemId}", OperationID: "SHADOW:OnPlaybackStopped", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodDelete, Pattern: "/Users/{userId}/PlayingItems/{itemId}", OperationID: "SHADOW:OnPlaybackStoppedLegacy", Policy: string(auth.PolicyDefaultAuthorization)},
	} {
		t.Run(want.OperationID, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), want.OperationID)
			if !found {
				t.Fatalf("operation %q is not registered", want.OperationID)
			}
			if got.Method != want.Method || got.Pattern != want.Pattern || got.Policy != want.Policy || got.Anonymous {
				t.Errorf("route %q = %#v, want non-anonymous %#v", want.OperationID, got, want)
			}
		})
	}
}

// Modern playback documents may contain an old registry SessionId that happens
// to equal their PlaySessionId. The authenticated actor, not either body value,
// selects the active session; PlaySessionId remains opaque report correlation.
func TestPlaystateStartMapsCaseInsensitiveBodyToAuthenticatedActor(t *testing.T) {
	service := &playstateHandlerFake{}
	rt := newPlaystateRouter(service)

	itemID := playstateHandlerUUID(t, "00000000-0000-0000-0000-000000000501")
	audio, subtitle := 3, 5
	position, started := int64(987_654), int64(123_456)
	volume, brightness := 41, 72
	want := playstate.Report{PlaybackState: session.PlaybackState{
		CanSeek:                true,
		ItemID:                 itemID,
		MediaSourceID:          "source-main",
		AudioStreamIndex:       &audio,
		SubtitleStreamIndex:    &subtitle,
		IsPaused:               true,
		IsMuted:                true,
		PositionTicks:          &position,
		PlaybackStartTimeTicks: &started,
		VolumeLevel:            &volume,
		Brightness:             &brightness,
		AspectRatio:            "16:9",
		PlayMethod:             "DirectStream",
		LiveStreamID:           "live-report",
		PlaySessionID:          "opaque-shared-id",
		RepeatMode:             "RepeatAll",
		PlaybackOrder:          "Shuffle",
		PlaylistItemID:         "playlist-entry",
	}}
	body := `{
		"cAnSeEk":true,
		"iTeMiD":"00000000-0000-0000-0000-000000000501",
		"mEdIaSoUrCeId":"source-main",
		"aUdIoStReAmInDeX":3,
		"sUbTiTlEsTrEaMiNdEx":5,
		"iSpAuSeD":true,
		"iSmUtEd":true,
		"pOsItIoNtIcKs":987654,
		"pLaYbAcKsTaRtTiMeTiCkS":123456,
		"vOlUmElEvEl":41,
		"bRiGhTnEsS":72,
		"aSpEcTrAtIo":"16:9",
		"pLaYmEtHoD":"DirectStream",
		"lIvEsTrEaMiD":"live-report",
		"sEsSiOnId":"opaque-shared-id",
		"pLaYsEsSiOnId":"opaque-shared-id",
		"rEpEaTmOdE":"RepeatAll",
		"pLaYbAcKoRdEr":"Shuffle",
		"pLaYlIsTiTeMiD":"playlist-entry"
	}`
	rec := playstateRequest(rt, http.MethodPost, "/Sessions/Playing?PositionTicks=7&PlayMethod=DirectPlay", body, "application/json")
	assertPlaystateStatus(t, rec, http.StatusNoContent)
	if got := rec.Body.Len(); got != 0 {
		t.Fatalf("start success body length = %d, want empty 204 response", got)
	}

	call := service.lastCall(t)
	if call.operation != "start" || call.actor != (playstate.Actor{UserID: dto.UUID{0x50, 0x1}, Client: "Authenticated Client", DeviceID: "authenticated-device"}) || !reflect.DeepEqual(call.report, want) {
		t.Fatalf("start call = %#v, want actor authenticated separately and report %#v", call, want)
	}
}

// The aliases bind transport scalars exactly once. With no transcoding job,
// ValidatePlayMethod turns the legacy omitted/Transcode method into DirectPlay;
// explicit DirectStream and DirectPlay remain typed by the shared query binder.
func TestPlaystateLegacyProgressBindsRouteAndQueryScalars(t *testing.T) {
	service := &playstateHandlerFake{}
	rt := newPlaystateRouter(service)

	itemID := "00000000-0000-0000-0000-000000000502"
	userID := "00000000-0000-0000-0000-000000000503"
	rec := playstateRequest(rt, http.MethodPost, "/Users/"+userID+"/PlayingItems/"+itemID+"/Progress?MediaSourceId=legacy-source&PositionTicks=54&CanSeek=true&IsPaused=true&IsMuted=true&PlaySessionId=legacy-correlation&RepeatMode=RepeatOne&PlaybackOrder=Default", "", "")
	assertPlaystateStatus(t, rec, http.StatusNoContent)

	position := int64(54)
	want := playstate.Report{PlaybackState: session.PlaybackState{
		ItemID:        playstateHandlerUUID(t, itemID),
		MediaSourceID: "legacy-source",
		PositionTicks: &position,
		CanSeek:       false,
		IsPaused:      true,
		IsMuted:       true,
		PlayMethod:    "DirectPlay",
		PlaySessionID: "legacy-correlation",
		RepeatMode:    "RepeatOne",
		PlaybackOrder: "Default",
	}}
	call := service.lastCall(t)
	if call.operation != "progress" || call.actor != (playstate.Actor{UserID: dto.UUID{0x50, 0x1}, Client: "Authenticated Client", DeviceID: "authenticated-device"}) || !reflect.DeepEqual(call.report, want) {
		t.Fatalf("legacy progress call = %#v, want %#v", call, want)
	}
}

// Modern reports carry non-nullable playback enums. Their JSON zero values must
// retain the source contract before the report reaches the playstate service:
// omitted and Transcode both mean DirectPlay without a transcoding job, while
// the other valid enum values pass through unchanged.
func TestPlaystateModernProgressNormalizesPlaybackEnums(t *testing.T) {
	itemID := "00000000-0000-0000-0000-000000000506"
	for _, tc := range []struct {
		name                                      string
		body                                      string
		wantPlayMethod, wantRepeatMode, wantOrder string
	}{
		{
			name:           "omitted enums use source zero values",
			body:           `{"ItemId":"` + itemID + `"}`,
			wantPlayMethod: "DirectPlay", wantRepeatMode: "RepeatNone", wantOrder: "Default",
		},
		{
			name:           "transcode method normalizes while explicit modes remain",
			body:           `{"ItemId":"` + itemID + `","PlayMethod":"Transcode","RepeatMode":"RepeatOne","PlaybackOrder":"Shuffle"}`,
			wantPlayMethod: "DirectPlay", wantRepeatMode: "RepeatOne", wantOrder: "Shuffle",
		},
		{
			name:           "direct stream remains unchanged",
			body:           `{"ItemId":"` + itemID + `","PlayMethod":"DirectStream","RepeatMode":"RepeatAll","PlaybackOrder":"Default"}`,
			wantPlayMethod: "DirectStream", wantRepeatMode: "RepeatAll", wantOrder: "Default",
		},
		{
			name:           "direct play remains unchanged",
			body:           `{"ItemId":"` + itemID + `","PlayMethod":"DirectPlay","RepeatMode":"RepeatNone","PlaybackOrder":"Shuffle"}`,
			wantPlayMethod: "DirectPlay", wantRepeatMode: "RepeatNone", wantOrder: "Shuffle",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), http.MethodPost, "/Sessions/Playing/Progress", tc.body, "application/json")
			assertPlaystateStatus(t, rec, http.StatusNoContent)

			call := service.lastCall(t)
			want := playstate.Report{PlaybackState: session.PlaybackState{
				ItemID:        playstateHandlerUUID(t, itemID),
				MediaSourceID: playstateHandlerUUID(t, itemID).String(),
				PlayMethod:    tc.wantPlayMethod,
				RepeatMode:    tc.wantRepeatMode,
				PlaybackOrder: tc.wantOrder,
			}}
			if call.operation != "progress" || !reflect.DeepEqual(call.report, want) {
				t.Fatalf("modern progress call = %#v, want %#v", call, want)
			}
		})
	}
}

// A missing PlayMethod binds to Transcode, and ValidatePlayMethod keeps
// Transcode only while a transcoding job runs for the PlaySessionId
// (PlaystateController.cs ValidatePlayMethod). Clients such as SharkParty omit
// PlayMethod for HLS remux and transcode, so the job decides the label.
func TestPlaystateValidatesPlayMethodAgainstTranscodingJobs(t *testing.T) {
	itemID := "00000000-0000-0000-0000-000000000507"
	for _, tc := range []struct {
		name, path, body, wantPlayMethod string
		jobs                             []string
	}{
		{
			name:           "omitted method with an active job is Transcode",
			path:           "/Sessions/Playing",
			body:           `{"ItemId":"` + itemID + `","PlaySessionId":"hls-session"}`,
			jobs:           []string{"hls-session"},
			wantPlayMethod: "Transcode",
		},
		{
			name:           "omitted method on progress with a job matched ignoring case is Transcode",
			path:           "/Sessions/Playing/Progress",
			body:           `{"ItemId":"` + itemID + `","PlaySessionId":"HLS-Session"}`,
			jobs:           []string{"hls-session"},
			wantPlayMethod: "Transcode",
		},
		{
			name:           "omitted method without a job is DirectPlay",
			path:           "/Sessions/Playing",
			body:           `{"ItemId":"` + itemID + `","PlaySessionId":"direct-session"}`,
			jobs:           []string{"hls-session"},
			wantPlayMethod: "DirectPlay",
		},
		{
			name:           "omitted method without a PlaySessionId is DirectPlay",
			path:           "/Sessions/Playing/Progress",
			body:           `{"ItemId":"` + itemID + `"}`,
			jobs:           []string{""},
			wantPlayMethod: "DirectPlay",
		},
		{
			name:           "explicit DirectStream stays with an active job",
			path:           "/Sessions/Playing/Progress",
			body:           `{"ItemId":"` + itemID + `","PlaySessionId":"hls-session","PlayMethod":"DirectStream"}`,
			jobs:           []string{"hls-session"},
			wantPlayMethod: "DirectStream",
		},
		{
			name:           "legacy start with omitted method and an active job is Transcode",
			path:           "/PlayingItems/" + itemID + "?PlaySessionId=hls-session",
			jobs:           []string{"hls-session"},
			wantPlayMethod: "Transcode",
		},
		{
			name:           "legacy progress with omitted method and no job is DirectPlay",
			path:           "/PlayingItems/" + itemID + "/Progress?PlaySessionId=other",
			jobs:           []string{"hls-session"},
			wantPlayMethod: "DirectPlay",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rt := newPlaystateRouterWithJobs(service, playstateJobsFake(tc.jobs))
			contentType := ""
			if tc.body != "" {
				contentType = "application/json"
			}
			rec := playstateRequest(rt, http.MethodPost, tc.path, tc.body, contentType)
			assertPlaystateStatus(t, rec, http.StatusNoContent)
			if got := service.lastCall(t).report.PlayMethod; got != tc.wantPlayMethod {
				t.Fatalf("PlayMethod = %q, want %q", got, tc.wantPlayMethod)
			}
		})
	}
}

type playstateJobsFake []string

func (jobs playstateJobsFake) HasTranscodingJob(playSessionID string) bool {
	for _, id := range jobs {
		if strings.EqualFold(id, playSessionID) {
			return true
		}
	}
	return false
}

// Stop has report-only Failed state and a narrower DTO than Start/Progress.
// It must not turn a body SessionId into a session selector, nor forward
// progress-only playback enums, even when the request supplies them.
func TestPlaystateStopPreservesFailedAndOpaquePlaySession(t *testing.T) {
	service := &playstateHandlerFake{}
	rt := newPlaystateRouter(service)

	rec := playstateRequest(rt, http.MethodPost, "/Sessions/Playing/Stopped", `{
		"ItemId":"00000000-0000-0000-0000-000000000504",
		"MediaSourceId":"stopped-source",
		"PositionTicks":88,
		"LiveStreamId":"stopped-live",
		"SessionId":"opaque-shared-id",
		"PlaySessionId":"opaque-shared-id",
		"PlaylistItemId":"stopped-playlist",
		"PlayMethod":"DirectStream",
		"RepeatMode":"RepeatAll",
		"PlaybackOrder":"Shuffle",
		"Failed":true
	}`, "application/json")
	assertPlaystateStatus(t, rec, http.StatusNoContent)

	position := int64(88)
	want := playstate.Report{PlaybackState: session.PlaybackState{
		ItemID:         playstateHandlerUUID(t, "00000000-0000-0000-0000-000000000504"),
		MediaSourceID:  "stopped-source",
		PositionTicks:  &position,
		LiveStreamID:   "stopped-live",
		PlaySessionID:  "opaque-shared-id",
		PlaylistItemID: "stopped-playlist",
	}, Failed: true}
	call := service.lastCall(t)
	if call.operation != "stop" || call.actor != (playstate.Actor{UserID: dto.UUID{0x50, 0x1}, Client: "Authenticated Client", DeviceID: "authenticated-device"}) || !reflect.DeepEqual(call.report, want) {
		t.Fatalf("stop call = %#v, want %#v", call, want)
	}
}

func TestPlaystateRoutesRejectInvalidReportsAndMapServiceErrors(t *testing.T) {
	itemID := "00000000-0000-0000-0000-000000000505"
	for _, tc := range []struct {
		name, method, path, body, contentType string
		serviceErr                            error
		wantStatus                            int
		wantCalls                             int
	}{
		{
			name: "malformed modern body", method: http.MethodPost, path: "/Sessions/Playing", body: `{`, contentType: "application/json", wantStatus: http.StatusBadRequest,
		},
		{
			name: "invalid legacy typed position", method: http.MethodPost, path: "/PlayingItems/" + itemID + "/Progress?PositionTicks=not-a-number", wantStatus: http.StatusBadRequest,
		},
		{
			name: "negative stop position", method: http.MethodPost, path: "/Sessions/Playing/Stopped", body: `{"ItemId":"` + itemID + `","PositionTicks":-1}`, contentType: "application/json", wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing start session", method: http.MethodPost, path: "/Sessions/Playing", body: `{"ItemId":"` + itemID + `"}`, contentType: "application/json", serviceErr: session.ErrNotFound, wantStatus: http.StatusNotFound, wantCalls: 1,
		},
		{
			name: "missing progress session is no op", method: http.MethodPost, path: "/Sessions/Playing/Progress", body: `{"ItemId":"` + itemID + `"}`, contentType: "application/json", serviceErr: session.ErrNotFound, wantStatus: http.StatusNoContent, wantCalls: 1,
		},
		{
			name: "missing stop session", method: http.MethodPost, path: "/Sessions/Playing/Stopped", body: `{"ItemId":"` + itemID + `"}`, contentType: "application/json", serviceErr: session.ErrNotFound, wantStatus: http.StatusNotFound, wantCalls: 1,
		},
		{
			name: "unexpected service error", method: http.MethodPost, path: "/Sessions/Playing", body: `{"ItemId":"` + itemID + `"}`, contentType: "application/json", serviceErr: errors.New("persistence unavailable"), wantStatus: http.StatusInternalServerError, wantCalls: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{err: tc.serviceErr}
			rec := playstateRequest(newPlaystateRouter(service), tc.method, tc.path, tc.body, tc.contentType)
			assertPlaystateStatus(t, rec, tc.wantStatus)
			if got := service.callCount(); got != tc.wantCalls {
				t.Fatalf("service calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

// Invalid playstate requests preserve the stock server's deliberately plain
// 400 transport, including the absence of the normal JSON nosniff header.
func TestPlaystateRoutesUseStockBadRequestTransport(t *testing.T) {
	itemID := "00000000-0000-0000-0000-000000000507"
	for _, tc := range []struct {
		name, path, body, contentType string
	}{
		{name: "malformed modern JSON", path: "/Sessions/Playing", body: `{`, contentType: "application/json"},
		{name: "negative modern stop position", path: "/Sessions/Playing/Stopped", body: `{"ItemId":"` + itemID + `","PositionTicks":-1}`, contentType: "application/json"},
		{name: "malformed legacy position", path: "/PlayingItems/" + itemID + "/Progress?PositionTicks=not-a-number"},
		{name: "missing ping play session", path: "/Sessions/Playing/Ping"},
		{name: "empty ping play session", path: "/Sessions/Playing/Ping?playSessionId="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), http.MethodPost, tc.path, tc.body, tc.contentType)

			if got := rec.Code; got != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body=%q", got, http.StatusBadRequest, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain" {
				t.Errorf("Content-Type = %q, want %q", got, "text/plain")
			}
			if got := rec.Body.String(); got != "Error processing request." {
				t.Errorf("body = %q, want exact stock body %q", got, "Error processing request.")
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
				t.Errorf("X-Content-Type-Options = %q, want absent", got)
			}
			if got := service.callCount(); got != 0 {
				t.Errorf("playstate application calls = %d, want no calls for rejected request", got)
			}
		})
	}
}

// Ping has no transcoder dependency yet. It still rejects absent correlation
// and must not accidentally invoke the playstate application.
func TestPlaystatePingRequiresCorrelationAndIsNoOp(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{name: "missing play session", path: "/Sessions/Playing/Ping", want: http.StatusBadRequest},
		{name: "blank play session", path: "/Sessions/Playing/Ping?playSessionId=", want: http.StatusBadRequest},
		{name: "present play session", path: "/Sessions/Playing/Ping?PlAySeSsIoNiD=correlation-42", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), http.MethodPost, tc.path, "", "")
			assertPlaystateStatus(t, rec, tc.want)
			if got := service.callCount(); got != 0 {
				t.Fatalf("ping invoked playstate application %d times, want no calls", got)
			}
		})
	}
}

// Reports that omit a media-source identifier still identify the item's
// canonical source. Both modern JSON and legacy route reports must pass the
// item's N-format ID to the playback application rather than an empty source.
func TestPlaystateReportsDefaultMissingMediaSourceToItemID(t *testing.T) {
	itemID := playstateHandlerUUID(t, "12345678-90ab-cdef-1234-567890abcdef")
	for _, tc := range []struct {
		name, path, body, contentType, operation string
	}{
		{
			name: "modern start", path: "/Sessions/Playing", body: `{"ItemId":"` + itemID.Dashed() + `"}`, contentType: "application/json", operation: "start",
		},
		{
			name: "legacy progress", path: "/PlayingItems/" + itemID.Dashed() + "/Progress", operation: "progress",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &playstateHandlerFake{}
			rec := playstateRequest(newPlaystateRouter(service), http.MethodPost, tc.path, tc.body, tc.contentType)
			assertPlaystateStatus(t, rec, http.StatusNoContent)

			call := service.lastCall(t)
			if call.operation != tc.operation {
				t.Fatalf("operation = %q, want %q", call.operation, tc.operation)
			}
			if got := call.report.PlaybackState.MediaSourceID; got != itemID.String() {
				t.Fatalf("missing media-source report value = %q, want item ID %q", got, itemID.String())
			}
		})
	}
}

// Playback ownership comes from the authenticated request identity. Device
// metadata in request headers is client-controlled and must not replace the
// resolved user, client, or device passed to the playback application.
func TestPlaystateReportForwardsResolvedUserIDDespiteSpoofedDeviceMetadata(t *testing.T) {
	service := &playstateHandlerFake{}
	identity := auth.Identity{
		Authenticated: true,
		UserID:        playstateHandlerUUID(t, "abcdefab-cdef-abcd-efab-cdefabcdefab"),
		Client:        "Resolved Client",
		DeviceID:      "resolved-device",
	}
	authorizer := middleware.NewAuthorizer(playstateHandlerResolver{identity: identity}, playstateHandlerEvaluator{}, playstateHandlerUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterPlaystate(rt, service)

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing", bytes.NewBufferString(`{"ItemId":"00000000-0000-0000-0000-000000000507","MediaSourceId":"source-main"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="Spoofed Client", Device="Spoofed Device", DeviceId="spoofed-device", Version="1.0"`)
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Spoofed Legacy Client", Device="Spoofed Legacy Device", DeviceId="spoofed-legacy-device", Version="1.0"`)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	assertPlaystateStatus(t, rec, http.StatusNoContent)

	call := service.lastCall(t)
	want := playstate.Actor{UserID: identity.UserID, Client: identity.Client, DeviceID: identity.DeviceID}
	if call.actor != want {
		t.Fatalf("playstate actor = %#v, want resolved identity %#v", call.actor, want)
	}
}

type playstateHandlerCall struct {
	operation string
	actor     playstate.Actor
	report    playstate.Report
}

type playstateHandlerFake struct {
	mu    sync.Mutex
	calls []playstateHandlerCall
	err   error
}

func (f *playstateHandlerFake) Start(_ context.Context, actor playstate.Actor, report playstate.Report) error {
	return f.record("start", actor, report)
}

func (f *playstateHandlerFake) Progress(_ context.Context, actor playstate.Actor, report playstate.Report) error {
	return f.record("progress", actor, report)
}

func (f *playstateHandlerFake) Stop(_ context.Context, actor playstate.Actor, report playstate.Report) error {
	return f.record("stop", actor, report)
}

func (f *playstateHandlerFake) record(operation string, actor playstate.Actor, report playstate.Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, playstateHandlerCall{operation: operation, actor: actor, report: report})
	return f.err
}

func (f *playstateHandlerFake) lastCall(t *testing.T) playstateHandlerCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("playstate application did not receive a report")
	}
	return f.calls[len(f.calls)-1]
}

func (f *playstateHandlerFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var _ playstate.Application = (*playstateHandlerFake)(nil)

func newPlaystateRouter(service playstate.Application) *router.Router {
	identity := auth.Identity{Authenticated: true, UserID: dto.UUID{0x50, 0x1}, Client: "Authenticated Client", DeviceID: "authenticated-device"}
	authorizer := middleware.NewAuthorizer(playstateHandlerResolver{identity: identity}, playstateHandlerEvaluator{}, playstateHandlerUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterPlaystate(rt, service)
	return rt
}

func newPlaystateRouterWithJobs(service playstate.Application, jobs handlers.TranscodingJobs) *router.Router {
	identity := auth.Identity{Authenticated: true, UserID: dto.UUID{0x50, 0x1}, Client: "Authenticated Client", DeviceID: "authenticated-device"}
	authorizer := middleware.NewAuthorizer(playstateHandlerResolver{identity: identity}, playstateHandlerEvaluator{}, playstateHandlerUsers{})
	rt := router.New(authorizer.Wrap)
	handlers.RegisterPlaystateWithGrants(rt, service, nil, nil, jobs)
	return rt
}

func playstateRequest(handler http.Handler, method, path, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertPlaystateStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, want, rec.Body.String())
	}
}

func playstateHandlerUUID(t *testing.T, value string) dto.UUID {
	t.Helper()
	id, err := dto.ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", value, err)
	}
	return id
}

type playstateHandlerResolver struct{ identity auth.Identity }

func (r playstateHandlerResolver) Resolve(context.Context, *http.Request) (auth.Identity, error) {
	return r.identity, nil
}

type playstateHandlerEvaluator struct{}

func (playstateHandlerEvaluator) Evaluate(context.Context, auth.PolicyRequest) (auth.Decision, error) {
	return auth.DecisionAllowed, nil
}

type playstateHandlerUsers struct{}

func (playstateHandlerUsers) Get(_ context.Context, id dto.UUID) (store.User, error) {
	return store.User{ID: id}, nil
}
