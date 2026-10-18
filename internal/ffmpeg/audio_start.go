// SPDX-License-Identifier: GPL-2.0-only

package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/sourcefile"
)

const audioStartProbeOutput = 1 << 20

// FirstPacketTicks reports the presentation time of the first packet of the
// ffmpeg input stream streamIndex within the first window of the source.
// found is false when the stream has no packet in the window. ffprobe
// -show_streams reports start_time 0 for a stream whose first packet lies
// beyond its probe window, so only a packet read finds a late track
// (deadfin#681). The caller keeps ownership of source.
func (p Prober) FirstPacketTicks(ctx context.Context, source *sourcefile.Source, streamIndex int, window time.Duration) (ticks int64, found bool, err error) {
	if source == nil {
		return 0, false, errors.New("ffprobe source is nil")
	}
	if strings.TrimSpace(p.Path) == "" {
		return 0, false, errors.New("ffprobe path is empty")
	}
	if streamIndex < 0 || window <= 0 {
		return 0, false, fmt.Errorf("invalid first-packet probe: stream %d, window %s", streamIndex, window)
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probe := p
	probe.MaxOutput = audioStartProbeOutput
	output, err := probe.probeJSON(commandCtx, firstPacketArgs(sourcefile.FDPath(probeSourceFD), window), source)
	if err != nil {
		return 0, false, err
	}
	if err := source.VerifyUnchanged(); err != nil {
		return 0, false, fmt.Errorf("probed source: %w", err)
	}
	return parseFirstPacket(output, streamIndex)
}

// firstPacketArgs reads the packets of every stream from the start of the
// input up to window and prints "stream_index,pts_time" lines. With
// -select_streams, ffprobe ends the interval only on a packet of the selected
// stream, so a late track would make it read up to that track's first packet.
func firstPacketArgs(input string, window time.Duration) []string {
	return []string{
		"-v", "error",
		"-read_intervals", "%+" + strconv.FormatFloat(window.Seconds(), 'f', -1, 64),
		"-show_entries", "packet=stream_index,pts_time",
		"-of", "csv=p=0",
		"-i", input,
	}
}

// parseFirstPacket returns the smallest presentation time of streamIndex in
// the output. Packets are in decode order, so the first line of a stream is
// not always its earliest.
func parseFirstPacket(output []byte, streamIndex int) (int64, bool, error) {
	earliest, found := int64(math.MaxInt64), false
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		stream, value, ok := strings.Cut(line, ",")
		if !ok {
			return 0, false, fmt.Errorf("parsing ffprobe packet line %q", line)
		}
		index, err := strconv.Atoi(strings.TrimSpace(stream))
		if err != nil {
			return 0, false, fmt.Errorf("parsing ffprobe packet stream %q", line)
		}
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ","))
		if index != streamIndex || value == "" || value == "N/A" {
			continue
		}
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return 0, false, fmt.Errorf("parsing ffprobe packet time %q", line)
		}
		earliest, found = min(earliest, int64(math.Round(seconds*10_000_000))), true
	}
	if err := scanner.Err(); err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	return earliest, true, nil
}
