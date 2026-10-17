// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

// These tests lock WP-P2-05 to Jellyfin b04614e18d2aeed8d3d3de920c381c6bd5b97bdb:
// MediaEncoder.cs:513-522 and EncoderValidator.cs:179-190,245-291.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

// fixtureTimeout bounds intentionally blocking fakes without making process
// startup itself a deadline race when the test host is oversubscribed.
const fixtureTimeout = 10 * time.Second

func TestProberPassesCompatibilityArgvAndReturnsOnlyStdout(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	t.Setenv("PROBE_STDOUT", `{"streams":[],"format":{}}`)
	path := writeFakeFFProbe(t, `
printf '%s\n' "$@" > "$ARGV_FILE"
printf '%s' "$PROBE_STDOUT"
printf 'diagnostic that must not corrupt JSON' >&2
`)

	got, err := (Prober{
		Path:            path,
		Timeout:         fixtureTimeout,
		MaxOutput:       1024,
		AnalyzeDuration: 200_000_000,
		ProbeSize:       1_000_000_000,
		ShowFrames:      true,
	}).Probe(context.Background(), "/media/with spaces/movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"streams":[],"format":{}}`; string(got) != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	args := readLines(t, argv)
	want := []string{"-analyzeduration", "200000000", "-probesize", "1000000000", "-v", "warning", "-print_format", "json", "-show_streams", "-show_data", "-show_chapters", "-show_format", "-show_frames", "-only_first_vframe", "-i", "/media/with spaces/movie.mkv"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ffprobe argv = %#v\nwant %#v", args, want)
	}
}

func TestProbeSourceUsesOnlyInheritedDescriptor(t *testing.T) {
	path := writeProbeSource(t, "movie.mkv", "pinned media bytes")
	source := openProbeSource(t, path)
	argv, bytesRead := filepath.Join(t.TempDir(), "argv"), filepath.Join(t.TempDir(), "fd3-bytes")
	t.Setenv("ARGV_FILE", argv)
	t.Setenv("FD3_BYTES_FILE", bytesRead)
	probe := writeFakeFFProbe(t, `
printf '%s\n' "$@" > "$ARGV_FILE"
cat /dev/fd/3 > "$FD3_BYTES_FILE"
printf '{"streams":[],"format":{}}'
`)

	got, err := (Prober{Path: probe, Timeout: fixtureTimeout, MaxOutput: 1024}).ProbeSource(context.Background(), source)
	if err != nil {
		t.Fatalf("ProbeSource(): %v", err)
	}
	if want := `{"streams":[],"format":{}}`; string(got) != want {
		t.Fatalf("ProbeSource() output = %q, want %q", got, want)
	}
	if got, want := string(readProbeFile(t, bytesRead)), "pinned media bytes"; got != want {
		t.Fatalf("child fd3 bytes = %q, want pinned source bytes %q", got, want)
	}
	args := readLines(t, argv)
	if got, want := args[len(args)-1], sourcefile.FDPath(3); got != want {
		t.Fatalf("ffprobe operand = %q, want inherited descriptor %q", got, want)
	}
	for _, arg := range args {
		if arg == path {
			t.Fatalf("ffprobe argv leaked real source path %q: %#v", path, args)
		}
	}
}

func TestProbeSourceMP3RewindsBeforeBothPasses(t *testing.T) {
	path := writeProbeSource(t, "song.mp3", "full mp3 source bytes")
	source := openProbeSource(t, path)
	calls, passOne, passTwo, argv := filepath.Join(t.TempDir(), "calls"), filepath.Join(t.TempDir(), "pass-one"), filepath.Join(t.TempDir(), "pass-two"), filepath.Join(t.TempDir(), "argv")
	t.Setenv("CALLS_FILE", calls)
	t.Setenv("PASS_ONE_FILE", passOne)
	t.Setenv("PASS_TWO_FILE", passTwo)
	t.Setenv("ARGV_FILE", argv)
	probe := writeFakeFFProbe(t, `
printf '%s\n' "$@" >> "$ARGV_FILE"
call=0
[ -f "$CALLS_FILE" ] && call=$(cat "$CALLS_FILE")
call=$((call + 1))
printf '%s' "$call" > "$CALLS_FILE"
case "$*" in
*-show_packets*)
	cat /dev/fd/3 > "$PASS_TWO_FILE"
	printf '{"packets":[{"stream_index":0,"duration":"1000","side_data_list":[{"side_data_type":"Skip Samples","skip_samples":"529","discard_padding":"10"}]}]}'
	;;
*)
	cat /dev/fd/3 > "$PASS_ONE_FILE"
	printf '{"streams":[{"index":0,"codec_name":"mp3","codec_type":"audio","duration":"1.000000","sample_rate":"48000","time_base":"1/1000"}],"format":{"format_name":"mp3","duration":"1.000000"}}'
	;;
esac
`)

	got, err := (Prober{Path: probe, Timeout: fixtureTimeout, MaxOutput: 4096}).ProbeSource(context.Background(), source)
	if err != nil {
		t.Fatalf("ProbeSource(): %v", err)
	}
	if !strings.Contains(string(got), `"deadfin_mp3_gapless"`) {
		t.Fatalf("ProbeSource() output = %s, want appended MP3 gapless facts", got)
	}
	for _, capture := range []string{passOne, passTwo} {
		if got, want := string(readProbeFile(t, capture)), "full mp3 source bytes"; got != want {
			t.Fatalf("child capture %s = %q, want complete source %q", capture, got, want)
		}
	}
	if got, want := string(readProbeFile(t, calls)), "2"; got != want {
		t.Fatalf("ffprobe launch count = %q, want two sequential passes", got)
	}
	fdOperands := 0
	for _, line := range readLines(t, argv) {
		if line == sourcefile.FDPath(3) {
			fdOperands++
		}
	}
	if fdOperands != 2 {
		t.Fatalf("inherited descriptor operand occurrences = %d, want two", fdOperands)
	}
	for _, line := range readLines(t, argv) {
		if line == path {
			t.Fatalf("ffprobe argv leaked real source path %q", path)
		}
	}
}

func TestProbeSourceRejectsSourceChangesAfterProbing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		setup func(*testing.T, string)
	}{
		{
			name: "atomic replacement",
			body: `mv "$REPLACEMENT_FILE" "$REAL_SOURCE_PATH"; printf '{"streams":[],"format":{}}'`,
			setup: func(t *testing.T, path string) {
				replacement := filepath.Join(t.TempDir(), "replacement.mkv")
				if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("REPLACEMENT_FILE", replacement)
				t.Setenv("REAL_SOURCE_PATH", path)
			},
		},
		{
			name:  "in-place mutation",
			body:  `printf x >> "$REAL_SOURCE_PATH"; printf '{"streams":[],"format":{}}'`,
			setup: func(t *testing.T, path string) { t.Setenv("REAL_SOURCE_PATH", path) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProbeSource(t, "movie.mkv", "original")
			source := openProbeSource(t, path)
			tc.setup(t, path)
			got, err := (Prober{Path: writeFakeFFProbe(t, tc.body), Timeout: fixtureTimeout, MaxOutput: 1024}).ProbeSource(context.Background(), source)
			if got != nil {
				t.Fatalf("ProbeSource() bytes = %q, want nil after stale source", got)
			}
			if !errors.Is(err, sourcefile.ErrStaleSource) {
				t.Fatalf("ProbeSource() error = %v, want ErrStaleSource", err)
			}
		})
	}
}

func TestProbeSourceMP3FallbackStillVerifiesSource(t *testing.T) {
	path := writeProbeSource(t, "song.mp3", "original")
	source := openProbeSource(t, path)
	t.Setenv("REAL_SOURCE_PATH", path)
	probe := writeFakeFFProbe(t, `
case "$*" in
*-show_packets*) printf x >> "$REAL_SOURCE_PATH"; printf '{bad packet payload' ;;
*) printf '{"streams":[{"index":0,"codec_name":"mp3","codec_type":"audio","duration":"1.000000","sample_rate":"48000","time_base":"1/1000"}],"format":{"format_name":"mp3","duration":"1.000000"}}' ;;
esac
`)
	got, err := (Prober{Path: probe, Timeout: fixtureTimeout, MaxOutput: 4096}).ProbeSource(context.Background(), source)
	if got != nil {
		t.Fatalf("ProbeSource() bytes = %q, want nil after fallback-side mutation", got)
	}
	if !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Fatalf("ProbeSource() error = %v, want ErrStaleSource", err)
	}
}

func TestProbeSourceErrorsPreserveSourceOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		body    string
		want    error
	}{
		{name: "cancellation", context: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, body: `: > "$LAUNCH_FILE"`, want: context.Canceled},
		{name: "output cap", context: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, body: `printf xxxxxxxxxxxxxxxxx`, want: ErrProbeOutputTooLarge},
		{name: "process failure", context: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, body: `exit 17`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeProbeSource(t, "movie.mkv", "original")
			source := openProbeSource(t, path)
			ctx, cancel := tc.context()
			defer cancel()
			_, err := (Prober{Path: writeFakeFFProbe(t, tc.body), Timeout: fixtureTimeout, MaxOutput: 16}).ProbeSource(ctx, source)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("ProbeSource() error = %v, want %v", err, tc.want)
				}
			} else if err == nil {
				t.Fatal("ProbeSource() process failure = nil")
			}
			if err := source.VerifyUnchanged(); err != nil {
				t.Fatalf("source ownership was not retained after ProbeSource error: %v", err)
			}
		})
	}
}

func TestProbeSourceNilAndClosedGuardsDoNotLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	t.Setenv("LAUNCH_FILE", marker)
	probe := writeFakeFFProbe(t, `: > "$LAUNCH_FILE"`)
	prober := Prober{Path: probe, Timeout: fixtureTimeout, MaxOutput: 1024}
	if _, err := prober.ProbeSource(context.Background(), nil); err == nil {
		t.Fatal("ProbeSource(nil) error = nil")
	}
	path := writeProbeSource(t, "movie.mkv", "original")
	source := openProbeSource(t, path)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := prober.ProbeSource(context.Background(), source); err == nil {
		t.Fatal("ProbeSource(closed source) error = nil")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ffprobe launched despite nil/closed source guard: %v", err)
	}
}

func TestProbeSourceAndValidateRejectsEmptyPayloadWithoutClosingSource(t *testing.T) {
	path := writeProbeSource(t, "movie.mkv", "original")
	source := openProbeSource(t, path)
	probe := writeFakeFFProbe(t, `printf '{}'`)
	got, err := (Prober{Path: probe, Timeout: fixtureTimeout, MaxOutput: 1024}).ProbeSourceAndValidate(context.Background(), source)
	if got != nil {
		t.Fatalf("ProbeSourceAndValidate() bytes = %q, want nil for an empty packet", got)
	}
	if !errors.Is(err, ErrProbeEmptyResult) {
		t.Fatalf("ProbeSourceAndValidate() error = %v, want %v", err, ErrProbeEmptyResult)
	}
	if err := source.VerifyUnchanged(); err != nil {
		t.Fatalf("source was closed or changed by payload validation: %v", err)
	}
}

func TestProberOmitsFrameFlagsWhenFirstFrameCapabilityIsUnavailable(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("ARGV_FILE", argv)
	path := writeFakeFFProbe(t, `
	printf '%s\n' "$@" > "$ARGV_FILE"
	printf '{"streams":[],"format":{}}'
	`)
	if _, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024, ShowFrames: false}).Probe(context.Background(), "/media/unsupported.mkv"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-v", "warning", "-print_format", "json", "-show_streams", "-show_data", "-show_chapters", "-show_format", "-i", "/media/unsupported.mkv"}
	if got := readLines(t, argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("ffprobe argv = %#v, want no -show_frames/-only_first_vframe %#v", got, want)
	}
}

func TestNewProberDiscoversFirstVideoFrameSupport(t *testing.T) {
	t.Run("supported option enables frames", func(t *testing.T) {
		argv := filepath.Join(t.TempDir(), "argv")
		t.Setenv("ARGV_FILE", argv)
		path := writeFakeFFProbe(t, `printf '%s\n' "$@" > "$ARGV_FILE"`)
		prober, err := NewProber(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if !prober.ShowFrames {
			t.Fatal("ShowFrames = false, want enabled when option probe exits zero")
		}
		if got, want := readLines(t, argv), []string{"-loglevel", "quiet", "-f", "lavfi", "-i", "nullsrc=s=1x1:d=1", "-only_first_vframe"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("support probe argv = %#v, want %#v", got, want)
		}
	})
	t.Run("unsupported option omits frames and ordinary probe succeeds", func(t *testing.T) {
		path := writeFakeFFProbe(t, `
		case "$*" in
		*-only_first_vframe*) exit 17 ;;
		*) printf '{"streams":[],"format":{}}' ;;
		esac`)
		prober, err := NewProber(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if prober.ShowFrames {
			t.Fatal("ShowFrames = true, want safe omission for unsupported option")
		}
		if _, err := prober.Probe(context.Background(), "/media/unsupported.mkv"); err != nil {
			t.Fatalf("ordinary probe after unsupported discovery: %v", err)
		}
	})
	t.Run("ordinary discovery process failure safely omits frames", func(t *testing.T) {
		path := writeFakeFFProbe(t, `exit 23`)
		prober, err := NewProber(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if prober.ShowFrames {
			t.Fatal("ShowFrames = true, want safe omission after process failure")
		}
	})
}

func TestNewProberDiscoveryCancellationAndTimeoutReapProcessTree(t *testing.T) {
	t.Run("pre-canceled context does not launch", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "launched")
		t.Setenv("LAUNCH_FILE", marker)
		path := writeFakeFFProbe(t, `: > "$LAUNCH_FILE"`)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewProber(ctx, path); !errors.Is(err, context.Canceled) {
			t.Fatalf("NewProber error = %v, want context cancellation", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discovery launched despite canceled context: %v", err)
		}
	})
	t.Run("deadline reaps descendants", func(t *testing.T) {
		parentPID, childPID := filepath.Join(t.TempDir(), "parent.pid"), filepath.Join(t.TempDir(), "child.pid")
		t.Setenv("PARENT_PID", parentPID)
		t.Setenv("CHILD_PID", childPID)
		reapPIDFilesOnCleanup(t, parentPID, childPID)
		path := writeFakeFFProbe(t, discoveryFixtureBody)
		ctx, cancel := newDeadlineContext()
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := NewProber(ctx, path); result <- err }()
		parent, child := waitForPID(t, parentPID), waitForPID(t, childPID)
		cancel()
		if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("NewProber error = %v, want deadline exceeded", err)
		}
		waitForProcessExit(t, parent)
		waitForProcessExit(t, child)
	})
}

func TestDiscoveryFixtureTethersDescendantToHarnessLifetime(t *testing.T) {
	if os.Getenv("DEADFIN_DISCOVERY_TETHER_HELPER") == "1" {
		runDiscoveryFixtureTetherHelper(t)
		return
	}

	root := t.TempDir()
	parentPID, childPID := filepath.Join(root, "parent.pid"), filepath.Join(root, "child.pid")
	command := exec.Command(os.Args[0], "-test.run=^TestDiscoveryFixtureTethersDescendantToHarnessLifetime$")
	command.Env = append(os.Environ(),
		"DEADFIN_DISCOVERY_TETHER_HELPER=1",
		"PARENT_PID="+parentPID,
		"CHILD_PID="+childPID,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helperExited := false
	t.Cleanup(func() {
		if !helperExited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		reapPIDFiles(parentPID, childPID)
	})
	child := waitForPID(t, childPID)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	helperExited = true
	if waitErr == nil {
		t.Fatal("discovery helper exited normally after SIGKILL")
	} else if exitError, ok := waitErr.(*exec.ExitError); !ok || !exitError.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exitError.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("discovery helper exit = %v, want SIGKILL", waitErr)
	}
	waitForProcessExit(t, child)
}

func TestDiscoveryFixtureTetherSurvivesHarnessDeathBeforeChildPublication(t *testing.T) {
	if os.Getenv("DEADFIN_DISCOVERY_TETHER_HELPER") == "1" {
		runDiscoveryFixtureTetherHelper(t)
		return
	}

	root := t.TempDir()
	parentPID, childPID := filepath.Join(root, "parent.pid"), filepath.Join(root, "child.pid")
	barrier := filepath.Join(root, "child-forked")
	releaseReader, releaseWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseClosed := false
	t.Cleanup(func() {
		_ = releaseReader.Close()
		if !releaseClosed {
			_ = releaseWriter.Close()
		}
	})

	command := exec.Command(os.Args[0], "-test.run=^TestDiscoveryFixtureTetherSurvivesHarnessDeathBeforeChildPublication$")
	command.ExtraFiles = []*os.File{releaseReader}
	command.Env = append(os.Environ(),
		"DEADFIN_DISCOVERY_TETHER_HELPER=1",
		"PARENT_PID="+parentPID,
		"CHILD_PID="+childPID,
		"DISCOVERY_TETHER_BARRIER_FILE="+barrier,
		"DISCOVERY_TETHER_RELEASE_FD=3",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helperExited := false
	t.Cleanup(func() {
		if !helperExited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		reapPIDFiles(parentPID, childPID)
	})

	waitForFile(t, barrier)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	helperExited = true
	if waitErr == nil {
		t.Fatal("discovery helper exited normally after SIGKILL")
	} else if exitError, ok := waitErr.(*exec.ExitError); !ok || !exitError.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exitError.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("discovery helper exit = %v, want SIGKILL", waitErr)
	}
	if err := releaseWriter.Close(); err != nil {
		t.Fatal(err)
	}
	releaseClosed = true
	waitForFile(t, barrier+".released")
	child := waitForPID(t, childPID)
	waitForProcessExit(t, child)
}

func runDiscoveryFixtureTetherHelper(t *testing.T) {
	t.Helper()
	parentPID, childPID := os.Getenv("PARENT_PID"), os.Getenv("CHILD_PID")
	if parentPID == "" || childPID == "" {
		t.Fatal("discovery tether helper needs parent and child PID paths")
	}
	path := writeFakeFFProbe(t, discoveryFixtureBody)
	if _, err := NewProber(context.Background(), path); err == nil {
		t.Fatal("NewProber returned before the outer test killed its harness")
	}
}

// A fast, bounded payload catches the StdoutPipe/Wait race where Wait closes
// the pipe before the reader has drained all bytes.
func TestProberPreservesLargeFastStdoutBelowLimit(t *testing.T) {
	path := writeFakeFFProbe(t, `
	dd if=/dev/zero bs=65536 count=8 2>/dev/null | tr '\000' x
	`)
	got, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1 << 20}).Probe(context.Background(), "/media/large.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("x", 8*65536); string(got) != want {
		t.Fatalf("stdout length/content = %d/%q, want %d x bytes", len(got), string(got[:min(len(got), 32)]), len(want))
	}
}

func TestProberLifecycleErrorsAreDistinct(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		path := writeFakeFFProbe(t, "sleep 2")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024}).Probe(ctx, "/media/input.mkv")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	})
	t.Run("hard timeout", func(t *testing.T) {
		path := writeFakeFFProbe(t, "sleep 2")
		_, err := (Prober{Path: path, Timeout: 20 * time.Millisecond, MaxOutput: 1024}).Probe(context.Background(), "/media/input.mkv")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	})
	t.Run("stdout is capped", func(t *testing.T) {
		path := writeFakeFFProbe(t, "printf 'xxxxxxxxxxxxxxxxx'")
		_, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 16}).Probe(context.Background(), "/media/input.mkv")
		if !errors.Is(err, ErrProbeOutputTooLarge) {
			t.Fatalf("error = %v, want %v", err, ErrProbeOutputTooLarge)
		}
	})
	t.Run("nonzero process status survives", func(t *testing.T) {
		path := writeFakeFFProbe(t, "printf bad-json; exit 17")
		_, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024}).Probe(context.Background(), "/media/input.mkv")
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want non-context process failure", err)
		}
	})
}

func TestPreCanceledProbeCommandsDoNotLaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, string) error
		fake func(*testing.T) string
	}{
		{name: "ffprobe", fake: func(t *testing.T) string { return writeFakeFFProbe(t, `: > "$LAUNCH_FILE"`) }, run: func(ctx context.Context, path string) error {
			_, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024}).Probe(ctx, "/media/input.mkv")
			return err
		}},
		{name: "ffmpeg capabilities", fake: writeFakeFFmpeg, run: func(ctx context.Context, path string) error {
			_, err := ProbeCapabilitiesWithOptions(ctx, path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 1024})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "launched")
			t.Setenv("LAUNCH_FILE", marker)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.run(ctx, tc.fake(t)); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pre-canceled command executed fake binary: stat %q = %v", marker, err)
			}
		})
	}
}

func TestProberTimeoutAndCancellationReapProcessTree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		timeout time.Duration
		want    error
	}{
		{name: "timeout", ctx: newDeadlineContext, timeout: fixtureTimeout, want: context.DeadlineExceeded},
		{name: "cancellation", ctx: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, timeout: fixtureTimeout, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentPID, childPID := filepath.Join(t.TempDir(), "parent.pid"), filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("PARENT_PID", parentPID)
			t.Setenv("CHILD_PID", childPID)
			reapPIDFilesOnCleanup(t, parentPID, childPID)
			path := writeFakeFFProbe(t, `
printf '%s\n' "$$" >> "$PARENT_PID"
sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$CHILD_PID"; printf child; printf child >&2; cat <&"$HARNESS_FD" >/dev/null' &
child=$!
printf parent
printf parent >&2
wait "$child"
`)
			ctx, cancel := tc.ctx()
			defer cancel()
			result := make(chan error, 1)
			completionDeadline := time.NewTimer(fixtureTimeout)
			defer completionDeadline.Stop()
			go func() {
				_, err := (Prober{Path: path, Timeout: tc.timeout, MaxOutput: 1024}).Probe(ctx, "/media/input.mkv")
				result <- err
			}()
			parent := waitForPID(t, parentPID)
			child := waitForPID(t, childPID)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
			case <-completionDeadline.C:
				killProcess(parent)
				killProcess(child)
				t.Fatal("Probe did not return within the process-tree cleanup deadline while descendant held inherited pipes")
			}
			waitForProcessExit(t, parent)
			waitForProcessExit(t, child)
		})
	}
}

func TestProberCancellationTakesPrecedenceOverOutputCap(t *testing.T) {
	release, emitted := filepath.Join(t.TempDir(), "release"), filepath.Join(t.TempDir(), "emitted")
	t.Setenv("RELEASE_FILE", release)
	t.Setenv("EMITTED_FILE", emitted)
	path := writeFakeFFProbe(t, `
while [ ! -e "$RELEASE_FILE" ]; do sleep 0.01; done
dd if=/dev/zero bs=8192 count=16 2>/dev/null
: > "$EMITTED_FILE"
cat <&"$HARNESS_FD" >/dev/null
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := (Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024}).Probe(ctx, "/media/input.mkv")
		result <- err
	}()
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, emitted)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation over %v", err, ErrProbeOutputTooLarge)
		}
	case <-time.After(fixtureTimeout):
		t.Fatal("Probe did not return after cancellation")
	}
}

func TestProbeAndValidateRejectsMalformedOrEmptyPayloads(t *testing.T) {
	path := writeFakeFFProbe(t, `printf '%s' "$PROBE_STDOUT"`)
	prober := Prober{Path: path, Timeout: fixtureTimeout, MaxOutput: 1024}
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{name: "empty object", body: `{}`, want: ErrProbeEmptyResult},
		{name: "unrelated json", body: `{"unexpected":true}`, want: ErrProbeEmptyResult},
		{name: "deceptive scalar fields", body: `{"streams":"not a stream array","format":"not a format object"}`, want: ErrProbeEmptyResult},
		{name: "malformed token payload", body: `{"streams":"quoted token","format":`, want: ErrProbeEmptyResult},
		{name: "stream payload", body: `{"streams":[]}`, want: nil},
		{name: "format payload", body: `{"format":{}}`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROBE_STDOUT", tc.body)
			_, err := prober.ProbeAndValidate(context.Background(), "/media/input.mkv")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestProbeCapabilitiesEnforcesPinnedMinimumAndEnumeratesCapabilities(t *testing.T) {
	path := writeFakeFFmpeg(t)
	t.Setenv("FFMPEG_VERSION", strings.Join([]string{
		"ffmpeg version 4.4-Jellyfin",
		"libavutil      56. 70.100 / 56. 70.100",
		"libavcodec     58.134.100 / 58.134.100",
		"libavformat    58. 76.100 / 58. 76.100",
		"libavdevice    58. 13.100 / 58. 13.100",
		"libavfilter     7.110.100 /  7.110.100",
		"libswscale      5.  9.100 /  5.  9.100",
		"libswresample   3.  9.100 /  3.  9.100",
	}, "\n"))
	caps, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := caps.Version, (Version{Major: 4, Minor: 4}); got != want {
		t.Fatalf("version = %#v, want %#v", got, want)
	}
	if got, want := caps.Encoders, []string{"h264", "hevc"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("encoders = %#v, want %#v", got, want)
	}
	if got, want := caps.Decoders, []string{"aac", "h264"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("decoders = %#v, want %#v", got, want)
	}
	if got, want := caps.Filters, []string{"scale", "yadif"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filters = %#v, want %#v", got, want)
	}
	if got, want := caps.BSFs, []string{"h264_mp4toannexb", "hevc_mp4toannexb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bsfs = %#v, want %#v", got, want)
	}
	if got, want := caps.HWAccels, []string{"cuda", "videotoolbox"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hwaccels = %#v, want %#v", got, want)
	}

	t.Setenv("FFMPEG_VERSION", strings.ReplaceAll(os.Getenv("FFMPEG_VERSION"), "ffmpeg version 4.4-Jellyfin", "ffmpeg version 4.3.2-Jellyfin"))
	_, err = ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if !errors.Is(err, ErrUnsupportedFFmpeg) {
		t.Fatalf("below-4.4 error = %v, want %v", err, ErrUnsupportedFFmpeg)
	}

	// EncoderValidator.GetFFmpegVersionInternal returns the 4.4 minimum when
	// a git/unknown header has every required library at the pinned floor.
	t.Setenv("FFMPEG_VERSION", strings.ReplaceAll(os.Getenv("FFMPEG_VERSION"), "ffmpeg version 4.3.2-Jellyfin", "ffmpeg version N-122128-gdeadbeef"))
	caps, err = ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if err != nil {
		t.Fatalf("unknown build with qualifying libraries: %v", err)
	}
	if got, want := caps.Version, (Version{Major: 4, Minor: 4}); got != want {
		t.Fatalf("fallback version = %#v, want %#v", got, want)
	}
}

// MediaEncoder.SetFFmpegPath enables the "p" pause key only when the "?" help
// of the binary lists it (MediaEncoder.cs:236). The probe sends "?" and then
// "q", so the null encode ends after the help.
func TestProbeCapabilitiesDetectsPKeyPauseFromRuntimeHelp(t *testing.T) {
	for _, test := range []struct {
		name string
		help string
		want bool
	}{
		{name: "jellyfin-ffmpeg", help: "c      Send command to first matching filter supporting it\np      pause transcoding\nq      quit\nu      unpause transcoding", want: true},
		{name: "stock ffmpeg", help: "c      Send command to first matching filter supporting it\nq      quit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdin := filepath.Join(t.TempDir(), "stdin")
			path := writeFakeFFmpegBody(t, `case "$2" in
-version) printf '%s\n' "$FFMPEG_VERSION" ;;
-f) cat > "$KEYS_STDIN"; printf '%s\n' "$KEYS_HELP" >&2; exit 0 ;;
*) exit 0 ;;
esac`)
			t.Setenv("FFMPEG_VERSION", qualifyingFFmpegVersion())
			t.Setenv("KEYS_STDIN", stdin)
			t.Setenv("KEYS_HELP", test.help)
			caps, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
			if err != nil {
				t.Fatal(err)
			}
			if caps.PKeyPause != test.want {
				t.Fatalf("PKeyPause = %t, want %t", caps.PKeyPause, test.want)
			}
			if got, err := os.ReadFile(stdin); err != nil || string(got) != "?q" {
				t.Fatalf("runtime-key probe stdin = %q (%v), want %q", got, err, "?q")
			}
		})
	}
}

// A binary that fails the runtime-key probe keeps the "c" fallback and still
// starts, as a failed CheckSupportedRuntimeKey returns false upstream.
func TestProbeCapabilitiesTreatsFailedRuntimeKeyProbeAsUnsupported(t *testing.T) {
	path := writeFakeFFmpeg(t)
	t.Setenv("FFMPEG_VERSION", qualifyingFFmpegVersion())
	caps, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if caps.PKeyPause {
		t.Fatal("PKeyPause = true for a binary that exits 23 on the runtime-key probe")
	}
}

func TestProbeCapabilitiesReapsDescendantsAfterDeadline(t *testing.T) {
	parentPID, childPID := filepath.Join(t.TempDir(), "cap-parent.pid"), filepath.Join(t.TempDir(), "cap-child.pid")
	t.Setenv("PARENT_PID", parentPID)
	t.Setenv("CHILD_PID", childPID)
	reapPIDFilesOnCleanup(t, parentPID, childPID)
	path := writeFakeFFmpegBody(t, `
case "$2" in
-version)
  printf '%s\n' "$$" >> "$PARENT_PID"
  sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$CHILD_PID"; cat <&"$HARNESS_FD" >/dev/null' &
  child=$!
  printf '%s\n' "$FFMPEG_VERSION"
  wait "$child"
  ;;
*) exit 23 ;;
esac`)
	t.Setenv("FFMPEG_VERSION", qualifyingFFmpegVersion())
	ctx, cancel := newDeadlineContext()
	defer cancel()
	result := make(chan error, 1)
	completionDeadline := time.NewTimer(fixtureTimeout)
	defer completionDeadline.Stop()
	go func() {
		_, err := ProbeCapabilitiesWithOptions(ctx, path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
		result <- err
	}()
	parent := waitForPID(t, parentPID)
	child := waitForPID(t, childPID)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
	case <-completionDeadline.C:
		killProcess(parent)
		killProcess(child)
		t.Fatal("capability command did not return within the WaitDelay cleanup deadline while descendant held inherited pipes")
	}
	waitForProcessExit(t, parent)
	waitForProcessExit(t, child)
}

func TestProbeCapabilitiesHonorsOptionTimeout(t *testing.T) {
	path := writeFakeFFmpegBody(t, `
case "$2" in
-version) cat <&"$HARNESS_FD" >/dev/null ;;
*) exit 23 ;;
esac`)
	_, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: 20 * time.Millisecond, MaxOutput: 4096})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

func TestProbeCapabilitiesParsesSingleNameBSFOutputWithoutLegend(t *testing.T) {
	path := writeFakeFFmpeg(t)
	t.Setenv("FFMPEG_VERSION", qualifyingFFmpegVersion())
	t.Setenv("FFMPEG_BSFS", "Bitstream filters:\nh264_mp4toannexb")
	caps, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := caps.BSFs, []string{"h264_mp4toannexb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BSFs = %#v, want %v", got, want)
	}
}

func qualifyingFFmpegVersion() string {
	return strings.Join([]string{
		"ffmpeg version 4.4-Jellyfin",
		"libavutil      56. 70.100 / 56. 70.100",
		"libavcodec     58.134.100 / 58.134.100",
		"libavformat    58. 76.100 / 58. 76.100",
		"libavdevice    58. 13.100 / 58. 13.100",
		"libavfilter     7.110.100 /  7.110.100",
		"libswscale      5.  9.100 /  5.  9.100",
		"libswresample   3.  9.100 /  3.  9.100",
	}, "\n")
}

func writeFakeFFmpegBody(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(fixtureShellHeader(t)+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeCapabilitiesIgnoresRealBSFLegendRows(t *testing.T) {
	path := writeFakeFFmpeg(t)
	t.Setenv("FFMPEG_VERSION", qualifyingFFmpegVersion())
	t.Setenv("FFMPEG_BSFS", "Bitstream filters:\nV..... = Video\nT.. = Timeline support\nh264_mp4toannexb")
	caps, err := ProbeCapabilitiesWithOptions(context.Background(), path, CapabilityOptions{Timeout: fixtureTimeout, MaxOutput: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := caps.BSFs, []string{"h264_mp4toannexb"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("BSFs = %#v, want %v; legend rows must never become capabilities", got, want)
	}
}

func writeProbeSource(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openProbeSource(t *testing.T, path string) *sourcefile.Source {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	size, modified := info.Size(), info.ModTime()
	source, err := sourcefile.Open(path, sourcefile.Expected{Size: &size, ModifiedAt: &modified})
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("Close(%q): %v", path, err)
		}
	})
	return source
}

func readProbeFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return data
}

func writeFakeFFProbe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(path, []byte(fixtureShellHeader(t)+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const discoveryFixtureBody = `
printf '%s\n' "$$" >> "$PARENT_PID"
sh -c 'trap "" TERM
if [ -n "${DISCOVERY_TETHER_BARRIER_FILE:-}" ]; then
  : > "$DISCOVERY_TETHER_BARRIER_FILE"
  cat <&"$DISCOVERY_TETHER_RELEASE_FD" >/dev/null
  : > "$DISCOVERY_TETHER_BARRIER_FILE.released"
fi
printf "%s\n" "$$" >> "$CHILD_PID"
cat <&"$HARNESS_FD" >/dev/null' &
wait "$!"`

// fixtureShellHeader gives each script an already-open read end of an
// anonymous pipe. The test binary is the only writer, so closing it on
// harness death releases fixture readers without a path-open race or sleep.
func fixtureShellHeader(t *testing.T) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := clearCloseOnExec(reader); err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(int(writer.Fd()))
	t.Setenv("HARNESS_FD", strconv.Itoa(int(reader.Fd())))
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return "#!/bin/sh\nset -eu\nHARNESS_PID=$PPID\nexport HARNESS_PID HARNESS_FD\n"
}

func clearCloseOnExec(file *os.File) error {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		return errno
	}
	_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_SETFD), flags&^uintptr(syscall.FD_CLOEXEC))
	if errno != 0 {
		return errno
	}
	return nil
}

func writeFakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	body := `#!/bin/sh
set -eu
[ -z "${LAUNCH_FILE:-}" ] || : > "$LAUNCH_FILE"
case "$2" in
-version) printf '%s\n' "$FFMPEG_VERSION" ;;
-encoders) printf ' V.... h264\n V.... hevc\n' ;;
-decoders) printf ' V.... h264\n A.... aac\n' ;;
-filters) printf ' ... scale V->V\n ... yadif V->V\n' ;;
-bsfs) printf '%s\n' "${FFMPEG_BSFS:- . h264_mp4toannexb
 . hevc_mp4toannexb}" ;;
-hwaccels) printf 'Hardware acceleration methods:\ncuda\nvideotoolbox\n' ;;
*) exit 23 ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	pids := waitForPIDs(t, path, 1)
	return pids[len(pids)-1]
}

func waitForPIDs(t *testing.T, path string, count int) []int {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			pids := make([]int, 0, len(fields))
			for _, field := range fields {
				pid, err := strconv.Atoi(field)
				if err != nil || pid <= 1 {
					t.Fatalf("invalid PID record %s: %q (%v)", path, field, err)
				}
				pids = append(pids, pid)
			}
			if len(pids) >= count {
				return pids
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d PID records in %s", count, path)
		case <-ticker.C:
		}
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(fixtureTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		select {
		case <-deadline.C:
			killProcess(pid)
			t.Fatalf("process %d survived cleanup", pid)
		case <-ticker.C:
		}
	}
}

type deadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newDeadlineContext() (context.Context, context.CancelFunc) {
	ctx := &deadlineContext{done: make(chan struct{})}
	return ctx, func() { ctx.once.Do(func() { close(ctx.done) }) }
}

func (ctx *deadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (ctx *deadlineContext) Done() <-chan struct{} { return ctx.done }

func (ctx *deadlineContext) Err() error {
	select {
	case <-ctx.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (ctx *deadlineContext) Value(any) any { return nil }

func killProcess(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// reapPIDFilesOnCleanup SIGKILLs every recorded fixture process group and
// member when the test ends, so an assertion failure cannot leak concurrent
// TERM-immune fixture jobs.
func reapPIDFilesOnCleanup(t *testing.T, paths ...string) {
	t.Helper()
	t.Cleanup(func() { reapPIDFiles(paths...) })
}

func reapPIDFiles(paths ...string) {
	pids := make(map[int]struct{})
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err == nil && pid > 1 {
				pids[pid] = struct{}{}
			}
		}
	}

	groups := make(map[int]struct{})
	for pid := range pids {
		if group, err := syscall.Getpgid(pid); err == nil && group > 1 {
			groups[group] = struct{}{}
		}
	}
	for group := range groups {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
