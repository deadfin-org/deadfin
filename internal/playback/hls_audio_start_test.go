// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

type fakeAudioStartProber struct {
	mu     sync.Mutex
	first  int64
	found  bool
	calls  int
	stream int
}

func (prober *fakeAudioStartProber) FirstPacketTicks(_ context.Context, source *sourcefile.Source, streamIndex int, window time.Duration) (int64, bool, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	prober.calls++
	prober.stream = streamIndex
	if source == nil || window <= 0 {
		return 0, false, os.ErrInvalid
	}
	return prober.first, prober.found, nil
}

// lateAudioSource is a remux-eligible source on a real file with a stereo
// AC3 track, as the Italian track of QA cell ATV-I18.
func lateAudioSource(t *testing.T) playback.MediaSourceInfo {
	t.Helper()
	media := filepath.Join(t.TempDir(), "Terminator 2 (1991).mkv")
	if err := os.WriteFile(media, []byte("matroska"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinned, err := sourcefile.Open(media, sourcefile.Expected{})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := pinned.Fingerprint()
	_ = pinned.Close()
	size, inode := fingerprint.Size, int64(fingerprint.Ino)
	modifiedAt, changedAt := fingerprint.MTime, fingerprint.CTime
	source := progressiveCopySource(media)
	source.FileFacts = playback.SourceFileFacts{Size: &size, ModifiedAt: &modifiedAt, Inode: &inode, ChangedAt: &changedAt}
	channels, bitrate := 2, 256_000
	source.MediaStreams[1].Channels = &channels
	source.MediaStreams[1].BitRate = &bitrate
	return source
}

func lateAudioRequest(audioCodecs ...string) playback.HLSRequest {
	request := hlsTranscodeRequest()
	request.AllowVideoStreamCopy = true
	request.AllowAudioStreamCopy = true
	request.EnableAutoStreamCopy = true
	request.AudioCodec = strings.Join(audioCodecs, ",")
	request.AudioCodecs = audioCodecs
	request.AudioBitrate = hlsInt(256_000)
	request.SegmentContainer = "mp4"
	return request
}

func lateAudioSegmentArgs(t *testing.T, prober *fakeAudioStartProber, source playback.MediaSourceInfo, request playback.HLSRequest) (string, []string) {
	t.Helper()
	manager := &hlsFakeManager{segment: "segment"}
	config := hlsServiceConfig(&progressiveFakeSource{source: source}, manager)
	config.AudioStart = prober
	service := playback.NewHLSService(config)
	master, err := service.Master(t.Context(), request)
	if err != nil {
		t.Fatalf("Master: %v", err)
	}
	playlist := readHLSResponse(t, master)
	request.SegmentIndex = 2
	request.RuntimeTicks = 120_000_000
	if _, err := service.Segment(t.Context(), request); err != nil {
		t.Fatalf("Segment: %v", err)
	}
	return playlist, manager.lastRestart.Args
}

// QA cell ATV-I18: the stereo AC3 track of Terminator 2 starts 33.55 s into
// the film. A copy left the first fMP4 segments without audio, and AVPlayer
// failed the item. The track is encoded and padded with silence from the job
// start instead.
func TestHLSServiceEncodesAndPadsAudioThatStartsLate(t *testing.T) {
	for _, test := range []struct {
		name        string
		codecs      []string
		wantEncoder string
		wantCodecs  string
	}{
		{name: "client accepts AAC", codecs: []string{"aac", "ac3"}, wantEncoder: "aac", wantCodecs: "mp4a.40.2"},
		{name: "client accepts only AC3", codecs: []string{"ac3"}, wantEncoder: "ac3", wantCodecs: "ac-3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prober := &fakeAudioStartProber{first: 335_500_000, found: true}
			playlist, args := lateAudioSegmentArgs(t, prober, lateAudioSource(t), lateAudioRequest(test.codecs...))
			if !strings.Contains(playlist, test.wantCodecs) {
				t.Errorf("master playlist = %q, want audio codec %s", playlist, test.wantCodecs)
			}
			if !hlsArgPair(args, "-codec:a:0", test.wantEncoder) {
				t.Errorf("segment argv = %q, want -codec:a:0 %s", args, test.wantEncoder)
			}
			// Segment 2 of 6 s segments starts at 12 s: 576000 samples at 48 kHz.
			if !hlsArgPair(args, "-af", "aresample=osr=48000:async=1:first_pts=576000") {
				t.Errorf("segment argv = %q, want the start pad filter", args)
			}
			if prober.calls != 1 || prober.stream != 1 {
				t.Errorf("first-packet probes = %d of stream %d, want one probe of stream 1", prober.calls, prober.stream)
			}
		})
	}
}

func TestHLSServiceCopiesAudioThatStartsOnTime(t *testing.T) {
	for _, test := range []struct {
		name  string
		first int64
		found bool
	}{
		{name: "starts at zero", first: 0, found: true},
		{name: "starts within one second", first: 9_000_000, found: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			prober := &fakeAudioStartProber{first: test.first, found: test.found}
			playlist, args := lateAudioSegmentArgs(t, prober, lateAudioSource(t), lateAudioRequest("aac", "ac3"))
			if !strings.Contains(playlist, "ac-3") || !hlsArgPair(args, "-codec:a:0", "copy") {
				t.Errorf("master = %q, argv = %q, want an AC3 copy", playlist, args)
			}
			for _, arg := range args {
				if strings.Contains(arg, "aresample") {
					t.Errorf("argv = %q, want no pad filter", args)
				}
			}
		})
	}
}

// A track with no packet in the probe window is late too. A client that
// accepts neither AAC nor the source codec keeps the copy.
func TestHLSServiceLateAudioWithoutEncodableCodecKeepsCopy(t *testing.T) {
	prober := &fakeAudioStartProber{found: false}
	source := lateAudioSource(t)
	source.MediaStreams[1].Codec = "dts"
	request := lateAudioRequest("dts")
	request.SegmentContainer = "mp4"
	_, args := lateAudioSegmentArgs(t, prober, source, request)
	if !hlsArgPair(args, "-codec:a:0", "copy") {
		t.Errorf("argv = %q, want the DTS copy kept", args)
	}
}
