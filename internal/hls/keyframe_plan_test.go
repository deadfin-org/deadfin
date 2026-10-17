// SPDX-License-Identifier: GPL-2.0-only

package hls_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/hls"
	"github.com/deadfin-org/deadfin/internal/keyframes"
)

func keyframePlanRequest(t *testing.T, keyframeTicks []int64) hls.MainPlaylistRequest {
	t.Helper()
	itemID, err := dto.ParseUUID("b6561ca4cfb64e7ca62e9166c0a42e30")
	if err != nil {
		t.Fatal(err)
	}
	return hls.MainPlaylistRequest{
		ItemID:                    itemID,
		MediaSourceID:             "primary",
		FilePath:                  "/media/feature.mkv",
		SegmentLengthMilliseconds: 6_000,
		RunTimeTicks:              300_000_000,
		IsRemuxVideo:              true,
		Keyframes:                 variantKeyframeSource{data: keyframes.Data{TotalDurationTicks: 300_000_000, KeyframeTicks: keyframeTicks}, found: true},
	}
}

// The plan carries the segments that CreateMainPlaylist advertises, with the
// source keyframes each one spans (deadfin#629).
func TestRemuxKeyframePlanSpansAdvertisedSegmentKeyframes(t *testing.T) {
	request := keyframePlanRequest(t, []int64{0, 30_000_000, 70_000_000, 90_000_000, 110_000_000, 130_000_000, 200_000_000, 202_000_000})
	plan, found, err := hls.RemuxKeyframePlan(t.Context(), request)
	if err != nil || !found {
		t.Fatalf("RemuxKeyframePlan() = found %v, error %v", found, err)
	}
	want := []hls.KeyframeSegment{
		{StartTicks: 0, LengthTicks: 70_000_000, FirstKeyframe: 0, EndKeyframe: 2},
		{StartTicks: 70_000_000, LengthTicks: 60_000_000, FirstKeyframe: 2, EndKeyframe: 5},
		{StartTicks: 130_000_000, LengthTicks: 70_000_000, FirstKeyframe: 5, EndKeyframe: 6},
		{StartTicks: 200_000_000, LengthTicks: 100_000_000, FirstKeyframe: 6, EndKeyframe: 8},
	}
	if !reflect.DeepEqual(plan.Segments, want) {
		t.Fatalf("plan segments = %+v, want %+v", plan.Segments, want)
	}

	playlist, err := hls.CreateMainPlaylist(t.Context(), func() hls.MainPlaylistRequest {
		request.SegmentContainer, request.EndpointPrefix = "ts", "hls1/main/"
		return request
	}())
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range plan.Segments {
		if !containsSegment(playlist, segment.StartTicks, segment.LengthTicks) {
			t.Errorf("playlist %q does not advertise plan segment %+v", playlist, segment)
		}
	}
}

func TestRemuxKeyframePlanRejectsPlansThatCannotCutPerKeyframe(t *testing.T) {
	for name, request := range map[string]hls.MainPlaylistRequest{
		"no keyframes":               keyframePlanRequest(t, nil),
		"first keyframe after a cut": keyframePlanRequest(t, []int64{70_000_000, 140_000_000}),
		"keyframes out of order":     keyframePlanRequest(t, []int64{0, 90_000_000, 70_000_000}),
		"transcode": func() hls.MainPlaylistRequest {
			r := keyframePlanRequest(t, []int64{0, 70_000_000})
			r.IsRemuxVideo = false
			return r
		}(),
		"source without keyframe data": func() hls.MainPlaylistRequest {
			r := keyframePlanRequest(t, []int64{0, 70_000_000})
			r.FilePath = "/media/feature.mp4"
			return r
		}(),
	} {
		if _, found, err := hls.RemuxKeyframePlan(t.Context(), request); found || err != nil {
			t.Errorf("%s: RemuxKeyframePlan() = found %v, error %v; want no plan", name, found, err)
		}
	}
}

func containsSegment(playlist string, startTicks, lengthTicks int64) bool {
	return strings.Contains(playlist, "runtimeTicks="+strconv.FormatInt(startTicks, 10)+"&actualSegmentLengthTicks="+strconv.FormatInt(lengthTicks, 10)+"\n")
}
