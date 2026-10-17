// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const (
	defaultProbeTimeout = 60 * time.Second
	defaultProbeOutput  = 32 << 20
	probeSourceFD       = 3
)

var (
	// ErrProbeOutputTooLarge means ffprobe exceeded the configured JSON response
	// bound. The child is still reaped and stderr is drained before it is returned.
	ErrProbeOutputTooLarge = errors.New("ffprobe output exceeds limit")
	// ErrProbeEmptyResult mirrors MediaEncoder's rejection of an ffprobe result
	// that contains neither stream nor format information.
	ErrProbeEmptyResult = errors.New("ffprobe returned no streams or format")
)

// Prober runs ffprobe as a one-shot, cancellation-aware process. Fields are
// configuration, not mutable process state, and are safe to copy before use.
type Prober struct {
	Path            string
	Timeout         time.Duration
	MaxOutput       int64
	AnalyzeDuration int64 // microseconds; zero leaves ffprobe's default intact.
	ProbeSize       int64 // bytes; zero leaves ffprobe's default intact.
	ShowFrames      bool  // set only after -only_first_vframe support discovery succeeds.
}

// NewProber discovers whether this ffprobe supports Jellyfin's
// -only_first_vframe option before enabling frame inspection. Unsupported or
// otherwise unusable binaries safely omit the option; caller cancellation is
// returned because startup cannot complete within its requested lifetime.
func NewProber(ctx context.Context, path string) (Prober, error) {
	prober := Prober{Path: path, Timeout: defaultProbeTimeout, MaxOutput: defaultProbeOutput}
	if err := ctx.Err(); err != nil {
		return Prober{}, err
	}
	supported, err := supportsFirstVideoFrame(ctx, prober)
	if err != nil {
		return Prober{}, err
	}
	prober.ShowFrames = supported
	return prober, nil
}

func supportsFirstVideoFrame(ctx context.Context, prober Prober) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, prober.Timeout)
	defer cancel()
	command := probeCommand(commandCtx, prober.Path, "-loglevel", "quiet", "-f", "lavfi", "-i", "nullsrc=s=1x1:d=1", "-only_first_vframe")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		if contextErr := commandCtx.Err(); contextErr != nil {
			return false, contextErr
		}
		return false, nil
	}
	waitErr := command.Wait()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err := commandCtx.Err(); err != nil {
		return false, err
	}
	return waitErr == nil, nil
}

// Probe runs ffprobe in warning/JSON mode and returns only its bounded stdout.
// MP3 inputs additionally receive a streaming packet scan that retains only
// aggregate duration and gapless side-data facts; it never buffers one record
// per packet. Stderr is intentionally discarded after draining: -v warning
// permits valid JSON alongside diagnostics, while callers receive process
// failures directly.
func (p Prober) Probe(ctx context.Context, inputPath string) ([]byte, error) {
	return p.run(ctx, inputPath, inputPath, nil)
}

// ProbeSource probes only the borrowed pinned source descriptor. Every child
// inherits the descriptor as fd 3 and addresses it through FDPath. After all
// child passes complete it verifies the descriptor, then the logical path; a
// stale source returns an error and no bytes. The caller retains ownership and
// must Close the source after this method returns.
func (p Prober) ProbeSource(ctx context.Context, source *sourcefile.Source) ([]byte, error) {
	if strings.TrimSpace(p.Path) == "" {
		return nil, fmt.Errorf("ffprobe path is empty")
	}
	if source == nil {
		return nil, errors.New("ffprobe source is nil")
	}

	output, err := p.run(ctx, sourcefile.FDPath(probeSourceFD), source.Path(), source)
	if err != nil {
		return nil, err
	}
	if err := source.VerifyUnchanged(); err != nil {
		return nil, fmt.Errorf("probed source: %w", err)
	}
	if err := source.VerifyPathStillCurrent(); err != nil {
		return nil, fmt.Errorf("probed source path: %w", err)
	}
	return output, nil
}

func (p Prober) run(ctx context.Context, operand, logicalPath string, source *sourcefile.Source) ([]byte, error) {
	if strings.TrimSpace(p.Path) == "" {
		return nil, fmt.Errorf("ffprobe path is empty")
	}
	if strings.TrimSpace(operand) == "" {
		return nil, fmt.Errorf("ffprobe input path is empty")
	}
	if p.Timeout <= 0 {
		p.Timeout = defaultProbeTimeout
	}
	if p.MaxOutput <= 0 {
		p.MaxOutput = defaultProbeOutput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	commandCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	output, err := p.probeJSON(commandCtx, p.args(operand), source)
	if err != nil || !strings.EqualFold(filepath.Ext(logicalPath), ".mp3") {
		return output, err
	}
	facts, err := p.mp3GaplessFacts(commandCtx, operand, output, source)
	if err != nil {
		if commandCtx.Err() != nil {
			return nil, commandCtx.Err()
		}
		// Packet side data is an optional compatibility refinement. A malformed
		// or unsupported packet listing must not discard otherwise valid source
		// metadata from the ordinary ffprobe invocation.
		return output, nil
	}
	if facts == nil {
		return output, nil
	}
	return appendMP3GaplessFacts(output, *facts)
}

func (p Prober) probeJSON(ctx context.Context, args []string, source *sourcefile.Source) ([]byte, error) {
	command := probeCommand(ctx, p.Path, args...)
	// ffprobe 8.0.1 and pinned jellyfin-ffmpeg 8.1.1 reject -nostdin as an
	// unknown option. Cmd.Stdin remains nil (the exec null device), preserving
	// non-interactive probes without an incompatible command-line flag.
	var output cappedBuffer
	output.limit = p.MaxOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if source != nil {
		command.ExtraFiles = []*os.File{source.File()}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source != nil {
		if err := source.Rewind(); err != nil {
			return nil, fmt.Errorf("rewinding ffprobe source: %w", err)
		}
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("starting ffprobe: %w", err)
	}
	waitErr := command.Wait()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.exceeded {
		return nil, ErrProbeOutputTooLarge
	}
	if waitErr != nil {
		return nil, fmt.Errorf("ffprobe: %w", waitErr)
	}
	return output.Bytes(), nil
}

const gaplessDurationPrecisionTicks = int64(10)

type mp3ProbeStream struct {
	Index      int    `json:"index"`
	CodecName  string `json:"codec_name"`
	CodecType  string `json:"codec_type"`
	Duration   string `json:"duration"`
	SampleRate string `json:"sample_rate"`
	TimeBase   string `json:"time_base"`
}

type mp3GaplessFact struct {
	UntrimmedTicks int64 `json:"untrimmed_ticks"`
	SkipSamples    int64 `json:"skip_samples"`
	DiscardPadding int64 `json:"discard_padding"`
	SampleRate     int64 `json:"sample_rate"`
}

// mp3GaplessFacts discovers the packet duration and encoder trimming facts
// without retaining a duration-proportional packet JSON array. The aggregate
// distinguishes old ffprobe's untrimmed duration from modern ffprobe's already
// corrected duration so padding is never subtracted twice.
func (p Prober) mp3GaplessFacts(ctx context.Context, inputPath string, raw []byte, source *sourcefile.Source) (*mp3GaplessFact, error) {
	var result struct {
		Streams []mp3ProbeStream `json:"streams"`
		Format  *struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Format == nil || !hasMP3Format(result.Format.FormatName) {
		return nil, nil
	}
	var audio *mp3ProbeStream
	for index := range result.Streams {
		stream := &result.Streams[index]
		if strings.EqualFold(stream.CodecType, "audio") {
			audio = stream
			break
		}
	}
	if audio == nil || !strings.EqualFold(audio.CodecName, "mp3") {
		return nil, nil
	}
	duration := audio.Duration
	if duration == "" {
		duration = result.Format.Duration
	}
	reportedTicks, ok := ffprobeDurationTicks(duration)
	if !ok {
		return nil, nil
	}
	sampleRate, err := strconv.ParseInt(strings.TrimSpace(audio.SampleRate), 10, 64)
	if err != nil || sampleRate <= 0 {
		return nil, nil
	}
	timeBaseNumerator, timeBaseDenominator, ok := ffprobeTimeBase(audio.TimeBase)
	if !ok {
		return nil, nil
	}
	packetDuration, skipSamples, discardPadding, err := p.scanMP3Packets(ctx, inputPath, audio.Index, source)
	if err != nil || packetDuration <= 0 || (skipSamples == 0 && discardPadding == 0) {
		return nil, err
	}
	untrimmedTicks, ok := packetDurationTicks(packetDuration, timeBaseNumerator, timeBaseDenominator)
	if !ok || math.Abs(float64(reportedTicks)-float64(untrimmedTicks)) > float64(gaplessDurationPrecisionTicks) {
		return nil, nil
	}
	return &mp3GaplessFact{UntrimmedTicks: untrimmedTicks, SkipSamples: skipSamples, DiscardPadding: discardPadding, SampleRate: sampleRate}, nil
}

func (p Prober) scanMP3Packets(ctx context.Context, inputPath string, streamIndex int, source *sourcefile.Source) (int64, int64, int64, error) {
	command := probeCommand(ctx, p.Path, "-v", "warning", "-print_format", "json", "-show_packets", "-show_entries", "packet=stream_index,duration,side_data_list", "-i", inputPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("opening ffprobe packet stdout: %w", err)
	}
	command.Stderr = io.Discard
	if source != nil {
		command.ExtraFiles = []*os.File{source.File()}
		if err := source.Rewind(); err != nil {
			return 0, 0, 0, fmt.Errorf("rewinding ffprobe packet source: %w", err)
		}
	}
	if err := command.Start(); err != nil {
		return 0, 0, 0, fmt.Errorf("starting ffprobe packet scan: %w", err)
	}
	duration, skip, discard, decodeErr := decodeMP3Packets(stdout, streamIndex)
	if decodeErr != nil {
		_ = command.Cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return 0, 0, 0, ctx.Err()
	}
	if decodeErr != nil {
		return 0, 0, 0, decodeErr
	}
	if waitErr != nil {
		return 0, 0, 0, fmt.Errorf("ffprobe packet scan: %w", waitErr)
	}
	return duration, skip, discard, nil
}

type ffprobePacketInt int64

func (value *ffprobePacketInt) UnmarshalJSON(raw []byte) error {
	var number int64
	if err := json.Unmarshal(raw, &number); err == nil {
		*value = ffprobePacketInt(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return fmt.Errorf("ffprobe packet integer: %w", err)
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("ffprobe packet integer %q: %w", text, err)
	}
	*value = ffprobePacketInt(parsed)
	return nil
}

func decodeMP3Packets(reader io.Reader, streamIndex int) (int64, int64, int64, error) {
	decoder := json.NewDecoder(reader)
	opening, err := decoder.Token()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("decoding ffprobe packets: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return 0, 0, 0, fmt.Errorf("decoding ffprobe packets: expected object")
	}
	var duration, skip, discard int64
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, 0, 0, fmt.Errorf("decoding ffprobe packet key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return 0, 0, 0, fmt.Errorf("decoding ffprobe packet key: expected string")
		}
		if key != "packets" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return 0, 0, 0, fmt.Errorf("decoding ffprobe packet field %q: %w", key, err)
			}
			continue
		}
		array, err := decoder.Token()
		if err != nil {
			return 0, 0, 0, fmt.Errorf("decoding ffprobe packets: %w", err)
		}
		if delimiter, ok := array.(json.Delim); !ok || delimiter != '[' {
			return 0, 0, 0, fmt.Errorf("decoding ffprobe packets: expected array")
		}
		for decoder.More() {
			var packet struct {
				StreamIndex int              `json:"stream_index"`
				Duration    ffprobePacketInt `json:"duration"`
				SideData    []struct {
					Type           string           `json:"side_data_type"`
					SkipSamples    ffprobePacketInt `json:"skip_samples"`
					DiscardPadding ffprobePacketInt `json:"discard_padding"`
				} `json:"side_data_list"`
			}
			if err := decoder.Decode(&packet); err != nil {
				return 0, 0, 0, fmt.Errorf("decoding ffprobe packet: %w", err)
			}
			if packet.StreamIndex != streamIndex {
				continue
			}
			if packet.Duration < 0 || duration > math.MaxInt64-int64(packet.Duration) {
				return 0, 0, 0, fmt.Errorf("ffprobe MP3 packet duration exceeds Int64")
			}
			duration += int64(packet.Duration)
			for _, data := range packet.SideData {
				if !strings.EqualFold(data.Type, "Skip Samples") {
					continue
				}
				if data.SkipSamples < 0 || data.DiscardPadding < 0 || skip > math.MaxInt64-int64(data.SkipSamples) || discard > math.MaxInt64-int64(data.DiscardPadding) {
					return 0, 0, 0, fmt.Errorf("ffprobe MP3 gapless padding exceeds Int64")
				}
				skip += int64(data.SkipSamples)
				discard += int64(data.DiscardPadding)
			}
		}
		if _, err := decoder.Token(); err != nil {
			return 0, 0, 0, fmt.Errorf("decoding ffprobe packet array: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, 0, 0, fmt.Errorf("decoding ffprobe packet object: %w", err)
	}
	return duration, skip, discard, nil
}

func hasMP3Format(value string) bool {
	for _, format := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(format), "mp3") {
			return true
		}
	}
	return false
}

func ffprobeDurationTicks(value string) (int64, bool) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(math.MaxInt64)/10_000_000 {
		return 0, false
	}
	return int64(math.Round(seconds * 10_000_000)), true
}

func ffprobeTimeBase(value string) (int64, int64, bool) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return 0, 0, false
	}
	numerator, numeratorErr := strconv.ParseInt(parts[0], 10, 64)
	denominator, denominatorErr := strconv.ParseInt(parts[1], 10, 64)
	if numeratorErr != nil || denominatorErr != nil || numerator <= 0 || denominator <= 0 {
		return 0, 0, false
	}
	return numerator, denominator, true
}

func packetDurationTicks(duration, numerator, denominator int64) (int64, bool) {
	seconds := float64(duration) * float64(numerator) / float64(denominator)
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(math.MaxInt64)/10_000_000 {
		return 0, false
	}
	return int64(math.Round(seconds * 10_000_000)), true
}

func appendMP3GaplessFacts(raw []byte, facts mp3GaplessFact) ([]byte, error) {
	encoded, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("encoding MP3 gapless facts: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("ffprobe metadata is not an object")
	}
	result := make([]byte, 0, len(trimmed)+len(encoded)+24)
	result = append(result, trimmed[:len(trimmed)-1]...)
	if len(trimmed) > 2 {
		result = append(result, ',')
	}
	result = append(result, `"deadfin_mp3_gapless":`...)
	result = append(result, encoded...)
	result = append(result, '}')
	return result, nil
}

func probeCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, path, args...)
	// Probe children can outlive ffprobe (for example, a demuxer helper). Give
	// every one-shot probe its own process group so cancellation reaps them too.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	return command
}

func (p Prober) args(inputPath string) []string {
	args := make([]string, 0, 18)
	if p.AnalyzeDuration > 0 {
		args = append(args, "-analyzeduration", fmt.Sprintf("%d", p.AnalyzeDuration))
	}
	if p.ProbeSize > 0 {
		args = append(args, "-probesize", fmt.Sprintf("%d", p.ProbeSize))
	}
	args = append(args,
		"-v", "warning",
		"-print_format", "json",
		"-show_streams",
		"-show_data",
		"-show_chapters",
		"-show_format",
	)
	if p.ShowFrames {
		// MediaEncoder.cs:519 asks ffprobe for exactly the first video frame;
		// later dynamic side data must not affect source-compatible normalization.
		args = append(args, "-show_frames", "-only_first_vframe")
	}
	return append(args, "-i", inputPath)
}

// ProbeAndValidate is the scanner-facing convenience seam. It keeps execution
// separate from pure probe normalization while rejecting a valid-but-empty JSON
// response before it reaches a caller that would persist no media facts.
func (p Prober) ProbeAndValidate(ctx context.Context, inputPath string) ([]byte, error) {
	output, err := p.Probe(ctx, inputPath)
	if err != nil {
		return nil, err
	}
	if !hasProbePayload(output) {
		return nil, ErrProbeEmptyResult
	}
	return output, nil
}

// ProbeSourceAndValidate is the descriptor-backed form of ProbeAndValidate.
// It borrows source and rejects a valid-but-empty response before callers can
// persist no media facts.
func (p Prober) ProbeSourceAndValidate(ctx context.Context, source *sourcefile.Source) ([]byte, error) {
	output, err := p.ProbeSource(ctx, source)
	if err != nil {
		return nil, err
	}
	if !hasProbePayload(output) {
		return nil, ErrProbeEmptyResult
	}
	return output, nil
}

func hasProbePayload(output []byte) bool {
	var result struct {
		Streams json.RawMessage `json:"streams"`
		Format  json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return false
	}
	return hasJSONContainer(result.Streams, '[') || hasJSONContainer(result.Format, '{')
}

func hasJSONContainer(value json.RawMessage, opening byte) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && trimmed[0] == opening
}
