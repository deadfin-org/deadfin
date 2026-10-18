// SPDX-License-Identifier: GPL-2.0-only

package playback_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/playback"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const extractSourceID = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"

// fakeExtractor writes deterministic content to every output path that the
// extraction or attachment arguments name.
type fakeExtractor struct {
	mu      sync.Mutex
	runs    [][]string
	content map[string]string // keyed by output extension or dump index
	err     error
}

func (extractor *fakeExtractor) Run(ctx context.Context, args []string, source *sourcefile.Source) error {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	extractor.runs = append(extractor.runs, append([]string(nil), args...))
	if source == nil {
		return errors.New("extractor source is nil")
	}
	if extractor.err != nil {
		return extractor.err
	}
	for index, arg := range args {
		switch {
		case arg == "-flush_packets" && index+2 < len(args):
			path := args[index+2]
			body := extractor.content[filepath.Ext(path)]
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return err
			}
		case strings.HasPrefix(arg, "-dump_attachment:") && index+1 < len(args):
			body := extractor.content[strings.TrimPrefix(arg, "-dump_attachment:")]
			if err := os.WriteFile(args[index+1], []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func (extractor *fakeExtractor) runCount() int {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	return len(extractor.runs)
}

func extractionFixture(t *testing.T) (playback.MediaSourceInfo, string) {
	t.Helper()
	dir := t.TempDir()
	media := filepath.Join(dir, "Show S01E01.mkv")
	if err := os.WriteFile(media, []byte("matroska"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "Show S01E01.en.srt")
	if err := os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return playback.MediaSourceInfo{
		ID:   extractSourceID,
		Path: media,
		MediaStreams: []playback.MediaStream{
			{Index: 0, Type: playback.StreamTypeSubtitle, Codec: "srt", IsExternal: true, SupportsExternalStream: true, Path: sidecar},
			{Index: 1, Type: playback.StreamTypeVideo, Codec: "h264"},
			{Index: 2, Type: playback.StreamTypeAudio, Codec: "flac"},
			{Index: 3, Type: playback.StreamTypeSubtitle, Codec: "ass", SupportsExternalStream: true},
			{Index: 4, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", SupportsExternalStream: true},
			{Index: 5, Type: playback.StreamTypeSubtitle, Codec: "subrip", SupportsExternalStream: true},
		},
		MediaAttachments: []playback.MediaAttachment{
			{Index: 6, Codec: "ttf", FileName: "Komika.ttf", MimeType: "application/x-truetype-font"},
			{Index: 7, Codec: "otf", FileName: "../escape.otf"},
			{Index: 8, Codec: "mjpeg", FileName: "cover.jpg", MimeType: "image/jpeg"},
			{Index: 9, Codec: "bin"},
		},
	}, dir
}

func readSubtitleResponse(t *testing.T, response playback.SubtitleResponse) string {
	t.Helper()
	if response.File == nil {
		t.Fatalf("response File = nil, want a streamed file (body %q)", response.Body)
	}
	defer func() { _ = response.File.Close() }()
	body, err := io.ReadAll(response.File)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != response.Size {
		t.Fatalf("response Size = %d, body has %d bytes", response.Size, len(body))
	}
	return string(body)
}

func TestSubtitleServiceExtractsEmbeddedASSAndPGSOnceAndServesThemUnchanged(t *testing.T) {
	source, _ := extractionFixture(t)
	cache := t.TempDir()
	const ass = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nStyle: Default,Arial,48\n\n[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\pos(320,50)}Sign\n"
	extractor := &fakeExtractor{content: map[string]string{".ass": ass, ".sup": "PG\x00\x00sup", ".srt": "1\n00:00:01,000 --> 00:00:02,000\nhi\n"}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources:        &subtitleServiceSource{source: source},
		Extractor:      extractor,
		ExtractionPath: cache,
	})

	response, err := service.Stream(t.Context(), subtitleStreamRequest(3, "ass"))
	if err != nil {
		t.Fatalf("Stream(ass): %v", err)
	}
	if response.ContentType != "text/x-ssa" {
		t.Errorf("ContentType = %q, want text/x-ssa", response.ContentType)
	}
	body := readSubtitleResponse(t, response)
	// SubtitleEncoder.SetAssFont renames Arial; everything else is byte for byte.
	if want := strings.Replace(ass, ",Arial,", ",Arial Unicode MS,", 1); body != want {
		t.Errorf("ASS body = %q, want %q", body, want)
	}

	folder := filepath.Join(cache, "0a", "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9")
	wantArgs := []string{
		"-nostdin", "-y", "-i", "/dev/fd/3",
		"-map", "0:2", "-an", "-vn", "-c:s", "copy", "-flush_packets", "1", filepath.Join(folder, "3.ass"),
		"-map", "0:3", "-an", "-vn", "-c:s", "copy", "-flush_packets", "1", filepath.Join(folder, "4.sup"),
		"-map", "0:4", "-an", "-vn", "-c:s", "copy", "-flush_packets", "1", filepath.Join(folder, "5.srt"),
	}
	if extractor.runCount() != 1 || !reflect.DeepEqual(extractor.runs[0], wantArgs) {
		t.Fatalf("extractor runs = %q, want one run %q", extractor.runs, wantArgs)
	}

	for _, format := range []string{"pgssub", "sup", "PGSSUB"} {
		response, err = service.Stream(t.Context(), subtitleStreamRequest(4, format))
		if err != nil {
			t.Fatalf("Stream(%s): %v", format, err)
		}
		if response.ContentType != "application/octet-stream" {
			t.Errorf("Stream(%s) ContentType = %q, want application/octet-stream", format, response.ContentType)
		}
		if got := readSubtitleResponse(t, response); got != "PG\x00\x00sup" {
			t.Errorf("Stream(%s) body = %q", format, got)
		}
	}
	if extractor.runCount() != 1 {
		t.Fatalf("extractor runs = %d after cached requests, want 1", extractor.runCount())
	}

	// A changed source makes the cache stale (IsCachedSubtitleFresh).
	if err := os.WriteFile(source.Path, []byte("matroska, remuxed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if response, err = service.Stream(t.Context(), subtitleStreamRequest(3, "ass")); err != nil {
		t.Fatalf("Stream(ass) after source change: %v", err)
	}
	_ = response.File.Close()
	if extractor.runCount() != 2 {
		t.Fatalf("extractor runs = %d after source change, want 2", extractor.runCount())
	}
}

func TestSubtitleServiceRejectsImageConversionAndKeepsTextConversion(t *testing.T) {
	source, _ := extractionFixture(t)
	extractor := &fakeExtractor{content: map[string]string{".ass": "[Script Info]\n", ".sup": "PG"}}
	converter := &subtitleServiceConverter{output: []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nflat\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources:        &subtitleServiceSource{source: source},
		Converter:      converter,
		Extractor:      extractor,
		ExtractionPath: t.TempDir(),
	})
	if _, err := service.Stream(t.Context(), subtitleStreamRequest(4, "srt")); !errors.Is(err, playback.ErrSubtitleInvalid) {
		t.Fatalf("Stream(PGS as srt) error = %v, want ErrSubtitleInvalid", err)
	}
	response, err := service.Stream(t.Context(), subtitleStreamRequest(3, "vtt"))
	if err != nil {
		t.Fatalf("Stream(ASS as vtt): %v", err)
	}
	if response.File != nil || !strings.Contains(string(response.Body), "flat") || converter.calls != 1 {
		t.Fatalf("Stream(ASS as vtt) = %#v with %d conversions, want converter output", response, converter.calls)
	}
	// SubtitleEncoder.GetReadableFile converts the extracted file, not the
	// media file.
	if extractor.runCount() != 1 {
		t.Fatalf("extractor runs = %d for a conversion, want 1", extractor.runCount())
	}
	if job := converter.jobs[0]; job.StreamIndex != -1 || !strings.HasSuffix(job.Source.Name(), "3.ass") {
		t.Fatalf("conversion job = stream %d of %q, want the extracted 3.ass", job.StreamIndex, job.Source.Name())
	}
}

// QA cell ATV-I18: an embedded SRT stream of a large remux timed out after
// 30 s because the per-request converter read the whole media file. Like
// SubtitleEncoder.GetReadableFile, the first request now waits for one
// extraction pass of every embedded stream, and every later request of any
// stream of the file converts its small cache file.
func TestSubtitleServiceConvertsEmbeddedTextFromTheExtractionCache(t *testing.T) {
	source, _ := extractionFixture(t)
	source.MediaStreams = append(source.MediaStreams, playback.MediaStream{Index: 10, Type: playback.StreamTypeSubtitle, Codec: "subrip", SupportsExternalStream: true})
	cache := t.TempDir()
	extractor := &fakeExtractor{content: map[string]string{".ass": "[Script Info]\n", ".sup": "PG", ".srt": "1\n00:00:01,000 --> 00:00:02,000\nhi\n"}}
	converter := &subtitleServiceConverter{output: []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n")}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources:        &subtitleServiceSource{source: source},
		Converter:      converter,
		Extractor:      extractor,
		ExtractionPath: cache,
	})
	for _, index := range []int{5, 10, 5} {
		response, err := service.Stream(t.Context(), subtitleStreamRequest(index, "srt"))
		if err != nil {
			t.Fatalf("Stream(%d as srt): %v", index, err)
		}
		if !strings.Contains(string(response.Body), "hi") {
			t.Fatalf("Stream(%d as srt) body = %q", index, response.Body)
		}
	}
	if extractor.runCount() != 1 {
		t.Fatalf("extractor runs = %d, want one pass for every stream", extractor.runCount())
	}
	folder := filepath.Join(cache, "0a", "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9")
	if converter.calls != 2 {
		t.Fatalf("conversions = %d, want 2 (the repeat request is a memory-cache hit)", converter.calls)
	}
	for call, want := range []string{"5.srt", "10.srt"} {
		job := converter.jobs[call]
		if job.StreamIndex != -1 || job.Source.Name() != filepath.Join(folder, want) {
			t.Fatalf("conversion %d = stream %d of %q, want %s", call, job.StreamIndex, job.Source.Name(), want)
		}
	}
}

// A stream that ffmpeg writes empty (a track with no cues) fails only its own
// request; the other outputs of the pass stay cached.
func TestSubtitleServiceKeepsOtherOutputsWhenOneExtractionIsEmpty(t *testing.T) {
	source, _ := extractionFixture(t)
	cache := t.TempDir()
	extractor := &fakeExtractor{content: map[string]string{".ass": "[Script Info]\n", ".sup": "PG"}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources:        &subtitleServiceSource{source: source},
		Converter:      &subtitleServiceConverter{output: []byte("x")},
		Extractor:      extractor,
		ExtractionPath: cache,
	})
	response, err := service.Stream(t.Context(), subtitleStreamRequest(3, "ass"))
	if err != nil {
		t.Fatalf("Stream(ass) with an empty SRT output: %v", err)
	}
	_ = response.File.Close()
	if _, err := service.Stream(t.Context(), subtitleStreamRequest(5, "srt")); err == nil {
		t.Fatal("Stream(empty srt) error = nil")
	}
	if response, err = service.Stream(t.Context(), subtitleStreamRequest(4, "sup")); err != nil {
		t.Fatalf("Stream(sup): %v", err)
	}
	_ = response.File.Close()
	// The empty stream is retried, as upstream retries a stream without a
	// cache file; the fresh ASS and PGS files are not extracted again.
	if got := extractor.runs[len(extractor.runs)-1]; extractor.runCount() != 3 || len(got) != 4+9 || !strings.HasSuffix(got[len(got)-1], "5.srt") {
		t.Fatalf("extractor runs = %q, want later passes for 5.srt only", extractor.runs)
	}
}

func TestSubtitleServiceServesExternalSUPUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Film.en.sup")
	if err := os.WriteFile(path, []byte("PG sidecar"), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := &subtitleServiceSource{source: subtitleSource(0)}
	sources.source.MediaStreams = []playback.MediaStream{{Index: 0, Type: playback.StreamTypeSubtitle, Codec: "PGSSUB", IsExternal: true, SupportsExternalStream: true, Path: path}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{Sources: sources})
	for _, format := range []string{"pgssub", "sup"} {
		response, err := service.Stream(t.Context(), subtitleStreamRequest(0, format))
		if err != nil {
			t.Fatalf("Stream(%s): %v", format, err)
		}
		if got := readSubtitleResponse(t, response); got != "PG sidecar" {
			t.Errorf("Stream(%s) body = %q", format, got)
		}
	}
	if _, err := service.Stream(t.Context(), subtitleStreamRequest(0, "vtt")); !errors.Is(err, playback.ErrSubtitleInvalid) {
		t.Fatalf("Stream(sup as vtt) error = %v, want ErrSubtitleInvalid", err)
	}
}

func TestSubtitleServiceExtractionFailureRemovesOutputs(t *testing.T) {
	source, _ := extractionFixture(t)
	cache := t.TempDir()
	extractor := &fakeExtractor{err: &ffmpeg.ExitError{Code: 1, Stderr: "boom"}}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: extractor, ExtractionPath: cache,
	})
	if _, err := service.Stream(t.Context(), subtitleStreamRequest(3, "ass")); err == nil {
		t.Fatal("Stream() error = nil after failed extraction")
	}
	entries, _ := filepath.Glob(filepath.Join(cache, "*", "*", "*"))
	if len(entries) != 0 {
		t.Fatalf("cache after failure = %q, want empty", entries)
	}
}

func TestSubtitleServiceExtractionNeedsGUIDSource(t *testing.T) {
	source, _ := extractionFixture(t)
	source.ID = "live-tv-stream"
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: &fakeExtractor{}, ExtractionPath: t.TempDir(),
	})
	if _, err := service.Stream(t.Context(), subtitleStreamRequest(3, "ass")); !errors.Is(err, playback.ErrExtractionUnavailable) {
		t.Fatalf("Stream() error = %v, want ErrExtractionUnavailable", err)
	}
}

func TestSubtitleServiceExtractionOutlivesTheRequest(t *testing.T) {
	source, _ := extractionFixture(t)
	release := make(chan struct{})
	started := make(chan struct{})
	extractor := &blockingExtractor{fakeExtractor: fakeExtractor{content: map[string]string{".ass": "[Script Info]\n", ".sup": "PG"}}, started: started, release: release}
	service := playback.NewSubtitleService(playback.SubtitleServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: extractor, ExtractionPath: t.TempDir(),
		ExtractionTimeout: func() time.Duration { return time.Hour },
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := service.Stream(ctx, subtitleStreamRequest(3, "ass"))
		done <- err
	}()
	<-started
	cancel()
	if err := extractor.contextErr(); err != nil {
		t.Fatalf("extraction context = %v after client cancel, want live (upstream passes CancellationToken.None)", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Stream(): %v", err)
	}
}

type blockingExtractor struct {
	fakeExtractor
	started, release chan struct{}
	ctx              context.Context
}

func (extractor *blockingExtractor) Run(ctx context.Context, args []string, source *sourcefile.Source) error {
	extractor.mu.Lock()
	extractor.ctx = ctx
	extractor.mu.Unlock()
	close(extractor.started)
	<-extractor.release
	return extractor.fakeExtractor.Run(ctx, args, source)
}

func (extractor *blockingExtractor) contextErr() error {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	return extractor.ctx.Err()
}

func TestAttachmentServiceDumpsOnceAndServesMimeType(t *testing.T) {
	source, _ := extractionFixture(t)
	cache := t.TempDir()
	extractor := &fakeExtractor{content: map[string]string{"6": "\x00\x01\x00\x00font", "7": "OTTO", "9": "blob"}}
	service := playback.NewAttachmentService(playback.AttachmentServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: extractor, Path: cache,
	})
	request := playback.AttachmentRequest{ItemID: subtitleServiceUUID("00000000-0000-0000-0000-000000000802"), MediaSourceID: extractSourceID, Index: 6}
	for range 2 {
		response, err := service.Attachment(t.Context(), request)
		if err != nil {
			t.Fatalf("Attachment(6): %v", err)
		}
		body, _ := io.ReadAll(response.File)
		_ = response.File.Close()
		if string(body) != "\x00\x01\x00\x00font" || response.ContentType != "application/x-truetype-font" || response.Size != int64(len(body)) {
			t.Fatalf("Attachment(6) = %q %q %d", body, response.ContentType, response.Size)
		}
	}
	folder := filepath.Join(cache, "0a", "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9")
	want := []string{"-nostdin", "-dump_attachment:6", filepath.Join(folder, "Komika.ttf.part"), "-i", "/dev/fd/3", "-t", "0", "-f", "null", "null"}
	if extractor.runCount() != 1 || !reflect.DeepEqual(extractor.runs[0], want) {
		t.Fatalf("extractor runs = %q, want one run %q", extractor.runs, want)
	}
	if _, err := os.Stat(filepath.Join(folder, "Komika.ttf")); err != nil {
		t.Fatalf("cached attachment: %v", err)
	}

	// A path in the file name keeps only its leaf; no name uses the index.
	request.Index = 7
	response, err := service.Attachment(t.Context(), request)
	if err != nil {
		t.Fatalf("Attachment(7): %v", err)
	}
	_ = response.File.Close()
	if response.ContentType != "application/octet-stream" {
		t.Errorf("Attachment(7) ContentType = %q, want application/octet-stream", response.ContentType)
	}
	if _, err := os.Stat(filepath.Join(folder, "escape.otf")); err != nil {
		t.Fatalf("leaf-named attachment: %v", err)
	}
	request.Index = 9
	if response, err = service.Attachment(t.Context(), request); err != nil {
		t.Fatalf("Attachment(9): %v", err)
	}
	_ = response.File.Close()
	if _, err := os.Stat(filepath.Join(folder, "9")); err != nil {
		t.Fatalf("index-named attachment: %v", err)
	}

	for _, index := range []int{8, 42} {
		request.Index = index
		if _, err := service.Attachment(t.Context(), request); !errors.Is(err, playback.ErrAttachmentNotFound) {
			t.Errorf("Attachment(%d) error = %v, want ErrAttachmentNotFound", index, err)
		}
	}
}

func TestAttachmentServiceFailureLeavesNoFile(t *testing.T) {
	source, _ := extractionFixture(t)
	cache := t.TempDir()
	service := playback.NewAttachmentService(playback.AttachmentServiceConfig{
		Sources: &subtitleServiceSource{source: source}, Extractor: &fakeExtractor{err: &ffmpeg.ExitError{Code: 1}}, Path: cache,
	})
	request := playback.AttachmentRequest{MediaSourceID: extractSourceID, Index: 6}
	if _, err := service.Attachment(t.Context(), request); err == nil || errors.Is(err, playback.ErrAttachmentNotFound) {
		t.Fatalf("Attachment() error = %v, want extraction failure", err)
	}
	entries, _ := filepath.Glob(filepath.Join(cache, "*", "*", "*"))
	if len(entries) != 0 {
		t.Fatalf("cache after failure = %q, want empty", entries)
	}
}
