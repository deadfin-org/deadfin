// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultCapabilityTimeout = 15 * time.Second
	defaultCapabilityOutput  = 8 << 20
)

var (
	// ErrUnsupportedFFmpeg reports an encoder that cannot meet Jellyfin's pinned
	// ffmpeg ABI floor. Continuing would defer a deterministic startup failure to
	// a playback request.
	ErrUnsupportedFFmpeg = errors.New("unsupported ffmpeg")
	// ErrCapabilityOutputTooLarge protects startup from a malformed executable.
	ErrCapabilityOutputTooLarge = errors.New("ffmpeg capability output exceeds limit")
)

// Version is an ffmpeg or libav version. Missing components are zero.
type Version struct {
	Major int
	Minor int
	Patch int
}

func (v Version) String() string {
	if v.Patch != 0 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

func (v Version) less(other Version) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

// CapabilityOptions bounds each startup command. Zero values use conservative
// defaults. The caller context remains authoritative for cancellation.
type CapabilityOptions struct {
	Timeout   time.Duration
	MaxOutput int64
}

// Capabilities is the immutable result of probing the installed ffmpeg binary.
// Slices are sorted for deterministic logs and test results.
type Capabilities struct {
	Version   Version
	Libraries map[string]Version
	Encoders  []string
	Decoders  []string
	Filters   []string
	BSFs      []string
	HWAccels  []string
	// BSFOptions lists the probed "filter=option" pairs of
	// BitStreamFilterOptions, for example "hevc_metadata=remove_dovi".
	BSFOptions []string
	// PKeyPause reports the "p" pause and "u" unpause runtime keys of
	// jellyfin-ffmpeg. Without them, the throttler falls back to the "c" key,
	// which does not stop the threaded pipeline of FFmpeg 7 and later.
	PKeyPause bool
}

// pKeyPauseHelp is the runtime-key help row that MediaEncoder.SetFFmpegPath
// checks (MediaEncoder.cs:236).
const pKeyPauseHelp = "p      pause transcoding"

// ProbeCapabilities interrogates an ffmpeg binary and enforces the pinned
// EncoderValidator.cs minimum library versions.
func ProbeCapabilities(ctx context.Context, path string) (Capabilities, error) {
	return ProbeCapabilitiesWithOptions(ctx, path, CapabilityOptions{})
}

// ProbeCapabilitiesWithOptions is ProbeCapabilities with bounded execution for
// callers that need tighter startup limits.
func ProbeCapabilitiesWithOptions(ctx context.Context, path string, options CapabilityOptions) (Capabilities, error) {
	if strings.TrimSpace(path) == "" {
		return Capabilities{}, fmt.Errorf("ffmpeg path is empty")
	}
	options = normalizeCapabilityOptions(options)
	versionOutput, err := runCapabilityCommand(ctx, path, options, "-version")
	if err != nil {
		return Capabilities{}, fmt.Errorf("ffmpeg -version: %w", err)
	}
	caps, err := parseCapabilitiesVersion(string(versionOutput))
	if err != nil {
		return Capabilities{}, err
	}

	for _, request := range []struct {
		arg  string
		into *[]string
	}{
		{"-encoders", &caps.Encoders},
		{"-decoders", &caps.Decoders},
		{"-filters", &caps.Filters},
		{"-bsfs", &caps.BSFs},
		{"-hwaccels", &caps.HWAccels},
	} {
		output, runErr := runCapabilityCommand(ctx, path, options, request.arg)
		if runErr != nil {
			return Capabilities{}, fmt.Errorf("ffmpeg %s: %w", request.arg, runErr)
		}
		*request.into = parseListedCapabilities(string(output), request.arg)
	}
	caps.PKeyPause = probeRuntimeKey(ctx, path, options, pKeyPauseHelp)
	caps.BSFOptions = probeBSFOptions(ctx, path, options, caps.BSFs)
	return caps, nil
}

// probeBSFOptions ports EncoderValidator.CheckBitStreamFilterWithOption
// (EncoderValidator.cs:517-543): "-h bsf=<filter>" must name the filter and
// list the option. A failed probe reports no support, as upstream does. A
// filter that -bsfs does not list is not probed.
func probeBSFOptions(ctx context.Context, path string, options CapabilityOptions, bsfs []string) []string {
	// The options of EncoderValidator._bsfOptionsDict
	// (EncoderValidator.cs:170-177), as "filter=option".
	entries := [...]string{
		"hevc_metadata=remove_dovi",
		"hevc_metadata=remove_hdr10plus",
		"av1_metadata=remove_dovi",
		"av1_metadata=remove_hdr10plus",
		"dovi_rpu=strip",
	}
	help := map[string][]byte{}
	var supported []string
	for _, entry := range entries {
		filter, option, _ := strings.Cut(entry, "=")
		if !slices.Contains(bsfs, filter) {
			continue
		}
		output, probed := help[filter]
		if !probed {
			output, _ = runProbeCommand(ctx, path, options, true, "-hide_banner", "-h", "bsf="+filter)
			help[filter] = output
		}
		if bytes.Contains(output, []byte("Bit stream filter "+filter)) && bytes.Contains(output, []byte(option)) {
			supported = append(supported, entry)
		}
	}
	return supported
}

// probeRuntimeKey ports EncoderValidator.CheckSupportedRuntimeKey
// (EncoderValidator.cs:545-568): it sends "?" to a null encode and searches the
// printed key help. The "q" that follows ends the encode after the help, so the
// probe does not wait for the input duration. A failed probe reports no
// support, as upstream does.
func probeRuntimeKey(ctx context.Context, path string, options CapabilityOptions, keyHelp string) bool {
	output, err := runProbeCommandInput(ctx, path, options, true, strings.NewReader("?q"),
		"-hide_banner", "-f", "lavfi", "-i", "nullsrc=s=1x1:d=10000", "-f", "null", "-")
	if err != nil {
		return false
	}
	return bytes.Contains(output, []byte(keyHelp))
}

func normalizeCapabilityOptions(options CapabilityOptions) CapabilityOptions {
	if options.Timeout <= 0 {
		options.Timeout = defaultCapabilityTimeout
	}
	if options.MaxOutput <= 0 {
		options.MaxOutput = defaultCapabilityOutput
	}
	return options
}

func runCapabilityCommand(ctx context.Context, path string, options CapabilityOptions, arg string) ([]byte, error) {
	return runProbeCommand(ctx, path, options, false, "-nostdin", arg)
}

// runProbeCommand runs one bounded ffmpeg probe. With allowExitStatus, a
// nonzero exit still returns the captured output, as upstream
// EncoderValidator.GetProcessOutput does.
func runProbeCommand(ctx context.Context, path string, options CapabilityOptions, allowExitStatus bool, args ...string) ([]byte, error) {
	return runProbeCommandInput(ctx, path, options, allowExitStatus, nil, args...)
}

// runProbeCommandInput is runProbeCommand with stdin read from input.
func runProbeCommandInput(ctx context.Context, path string, options CapabilityOptions, allowExitStatus bool, input io.Reader, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	command := exec.Command(path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = time.Second
	var stdout cappedBuffer
	stdout.limit = options.MaxOutput
	command.Stdout = &stdout
	command.Stderr = &stdout // version/help headers are emitted on stderr by some builds.
	command.Stdin = input
	if err := commandCtx.Err(); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	processDone := make(chan struct{})
	go func(pid int) {
		select {
		case <-commandCtx.Done():
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-processDone:
		}
	}(command.Process.Pid)
	err := command.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	close(processDone)
	if commandCtx.Err() != nil {
		return nil, commandCtx.Err()
	}
	if stdout.exceeded {
		return nil, ErrCapabilityOutputTooLarge
	}
	var exitError *exec.ExitError
	if err != nil && !(allowExitStatus && errors.As(err, &exitError)) {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// VAAPI driver names reported by ProbeVaapiDriver.
const (
	VaapiDriverIntelIHD  = "iHD"
	VaapiDriverIntelI965 = "i965"
)

// ProbeVaapiDriver opens the VAAPI render node with ffmpeg and returns the
// Intel driver it reports, or an empty string for any other driver. It ports
// EncoderValidator.CheckVaapiDeviceByDriverName (EncoderValidator.cs:403-425)
// as MediaEncoder.SetFFmpegPath uses it (MediaEncoder.cs:239-249).
func ProbeVaapiDriver(ctx context.Context, path, renderNode string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(renderNode) == "" {
		return "", fmt.Errorf("ffmpeg path and VAAPI render node are required")
	}
	output, err := runProbeCommand(ctx, path, normalizeCapabilityOptions(CapabilityOptions{}), true,
		"-nostdin", "-v", "verbose", "-hide_banner", "-init_hw_device", "vaapi=va:"+renderNode)
	if err != nil {
		return "", fmt.Errorf("ffmpeg VAAPI probe: %w", err)
	}
	return parseVaapiDriver(string(output)), nil
}

func parseVaapiDriver(output string) string {
	switch {
	case strings.Contains(output, "Intel iHD driver"):
		return VaapiDriverIntelIHD
	case strings.Contains(output, "Intel i965 driver"):
		return VaapiDriverIntelI965
	}
	return ""
}

type cappedBuffer struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data.Bytes())
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(b.data.Len()) >= b.limit {
		b.exceeded = true
		return len(p), nil
	}
	remaining := b.limit - int64(b.data.Len())
	if int64(len(p)) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.data.Write(p)
}

var (
	ffmpegVersionPattern = regexp.MustCompile(`(?mi)^ffmpeg version n?([0-9]+(?:\.[0-9]+){0,2})`)
	libraryPattern       = regexp.MustCompile(`(?mi)^(lib\w+)\s+([0-9]+)\.\s*([0-9]+)`)
	// filterPattern ports EncoderValidator.FilterRegex. FFmpeg 7.1 and
	// later print two flag columns (T, S); earlier releases print three
	// (T, S, C).
	filterPattern = regexp.MustCompile(`^\s\S{2,3}\s([\w|-]+)\s+.+$`)
)

func minimumLibraries() []struct {
	name    string
	minimum Version
} {
	return []struct {
		name    string
		minimum Version
	}{
		{"libavutil", Version{Major: 56, Minor: 70}},
		{"libavcodec", Version{Major: 58, Minor: 134}},
		{"libavformat", Version{Major: 58, Minor: 76}},
		{"libavdevice", Version{Major: 58, Minor: 13}},
		{"libavfilter", Version{Major: 7, Minor: 110}},
		{"libswscale", Version{Major: 5, Minor: 9}},
		{"libswresample", Version{Major: 3, Minor: 9}},
	}
}

func parseCapabilitiesVersion(output string) (Capabilities, error) {
	if strings.Contains(strings.ToLower(output), "libav developers") {
		return Capabilities{}, fmt.Errorf("%w: avconv is not supported", ErrUnsupportedFFmpeg)
	}
	caps := Capabilities{Libraries: make(map[string]Version)}
	if match := ffmpegVersionPattern.FindStringSubmatch(output); len(match) == 2 {
		version, ok := parseVersion(match[1])
		if !ok {
			return Capabilities{}, fmt.Errorf("%w: invalid ffmpeg version %q", ErrUnsupportedFFmpeg, match[1])
		}
		caps.Version = version
	}
	for _, match := range libraryPattern.FindAllStringSubmatch(output, -1) {
		major, _ := strconv.Atoi(match[2])
		minor, _ := strconv.Atoi(match[3])
		caps.Libraries[match[1]] = Version{Major: major, Minor: minor}
	}
	for _, requirement := range minimumLibraries() {
		name, minimum := requirement.name, requirement.minimum
		actual, ok := caps.Libraries[name]
		if !ok {
			return Capabilities{}, fmt.Errorf("%w: %s version not found", ErrUnsupportedFFmpeg, name)
		}
		if actual.less(minimum) {
			return Capabilities{}, fmt.Errorf("%w: %s %s is below required %s", ErrUnsupportedFFmpeg, name, actual, minimum)
		}
	}
	// EncoderValidator.GetFFmpegVersionInternal returns MinVersion when a
	// vendor/git build omits a parseable ffmpeg header but every pinned library
	// ABI floor is present (EncoderValidator.cs:340-378).
	if caps.Version == (Version{}) {
		caps.Version = Version{Major: 4, Minor: 4}
	}
	if caps.Version.Major != 0 && caps.Version.less(Version{Major: 4, Minor: 4}) {
		return Capabilities{}, fmt.Errorf("%w: ffmpeg %s is below required 4.4", ErrUnsupportedFFmpeg, caps.Version)
	}
	return caps, nil
}

func parseVersion(value string) (Version, bool) {
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, false
	}
	var result Version
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return Version{}, false
		}
		switch index {
		case 0:
			result.Major = parsed
		case 1:
			result.Minor = parsed
		case 2:
			result.Patch = parsed
		}
	}
	return result, true
}

func parseListedCapabilities(output, command string) []string {
	var result []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var candidate string
		switch command {
		case "-filters":
			if match := filterPattern.FindStringSubmatch(strings.TrimRight(line, "\r")); match != nil {
				candidate = match[1]
			}
		case "-bsfs":
			if len(fields) == 1 && isCapabilityName(fields[0]) {
				candidate = fields[0]
			} else if len(fields) >= 2 && fields[0] == "." && isCapabilityName(fields[1]) {
				candidate = fields[1]
			}
		case "-hwaccels":
			if len(fields) == 1 && fields[0] != "Hardware" && fields[0] != "acceleration" && fields[0] != "methods:" {
				candidate = fields[0]
			}
		default:
			if len(fields) >= 2 && strings.ContainsAny(fields[0], "VASD.FX") {
				candidate = fields[1]
			}
		}
		if candidate != "" && candidate != "=" {
			result = append(result, candidate)
		}
	}
	sort.Strings(result)
	return result
}

func isCapabilityName(value string) bool {
	if value == "" || strings.Contains(value, "=") {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') &&
			(r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') &&
			r != '_' && r != '-' {
			return false
		}
	}
	return true
}
