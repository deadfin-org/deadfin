// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"fmt"
	"math"
	"strings"
)

// cell is one row of the Apple TV QA matrix. The matrix is not a cartesian
// product: each cell pins one high-risk combination, and the cells together
// cover every value of every dimension at least once and the risky pairs
// (container x video, video x player, audio x player, subtitle x player).
type cell struct {
	ID    string
	Group string
	Title string
	// Dims names the dimension values the cell pins. Keys: container,
	// video, range, fps, res, audio, subs, ops, delivery, player.
	Dims map[string]string
	// Risk says why the cell exists.
	Risk string
	// Pass says what passing means on the TV.
	Pass string
	// Human is non-empty when the cell needs an operator's eye that no
	// probe can replace.
	Human  string
	Expect map[string]Expect
	// Minutes is the auto-probe play time.
	Minutes int
	// Entries are the start points, in seconds, that qamatrix check
	// fetches in addition to the start and mid-file runs: a resume or a far
	// seek. A negative value counts from the end.
	Entries []float64
	// Missing says how to source an item when the library has none.
	Missing string

	// pinned cells hold specific known-bad items. map takes them from
	// -pin or from the local matrix; match is the fallback.
	pinned   bool
	match    func(it item, ms mediaSource) bool
	audio    func(ms mediaSource) int // nil: server default
	subtitle func(ms mediaSource) int // nil: off
}

// Stream helpers. They read the ffprobe-derived MediaStreams only.

func videoOf(ms mediaSource) *mediaStream {
	for i := range ms.MediaStreams {
		if ms.MediaStreams[i].Type == "Video" {
			return &ms.MediaStreams[i]
		}
	}
	return nil
}

func streamsOf(ms mediaSource, typ string) []mediaStream {
	var out []mediaStream
	for _, s := range ms.MediaStreams {
		if s.Type == typ {
			out = append(out, s)
		}
	}
	return out
}

func containerIs(ms mediaSource, names ...string) bool {
	for _, part := range strings.Split(ms.Container, ",") {
		for _, n := range names {
			if strings.EqualFold(part, n) {
				return true
			}
		}
	}
	return false
}

func isMP4(ms mediaSource) bool { return containerIs(ms, "mp4", "m4v", "mov") }

func vid(ms mediaSource, codec string, f func(v mediaStream) bool) bool {
	v := videoOf(ms)
	if v == nil || !strings.EqualFold(v.Codec, codec) {
		return false
	}
	return f == nil || f(*v)
}

func rangeIs(v mediaStream, t string) bool { return v.VideoRangeType == t }

func fpsNear(v mediaStream, want float64) bool { return math.Abs(v.RealFrameRate-want) < 0.01 }

func is1080(v mediaStream) bool { return v.Height > 720 && v.Height <= 1088 }
func is4K(v mediaStream) bool   { return v.Height > 1088 }
func isSD(v mediaStream) bool   { return v.Height > 0 && v.Height <= 576 }

// onlyAudio reports whether the source has exactly one audio track and it
// matches f.
func onlyAudio(ms mediaSource, f func(a mediaStream) bool) bool {
	a := streamsOf(ms, "Audio")
	return len(a) == 1 && f(a[0])
}

func firstAudio(ms mediaSource, f func(a mediaStream) bool) bool {
	a := streamsOf(ms, "Audio")
	return len(a) > 0 && f(a[0])
}

func codec(c string) func(a mediaStream) bool {
	return func(a mediaStream) bool { return strings.EqualFold(a.Codec, c) }
}

func audioCh(c string, ch int) func(a mediaStream) bool {
	return func(a mediaStream) bool { return strings.EqualFold(a.Codec, c) && a.Channels == ch }
}

func noSubs(ms mediaSource) bool { return len(streamsOf(ms, "Subtitle")) == 0 }

func hasSub(ms mediaSource, f func(s mediaStream) bool) bool { return subIndex(ms, f) >= 0 }

func subIndex(ms mediaSource, f func(s mediaStream) bool) int {
	for _, s := range streamsOf(ms, "Subtitle") {
		if f(s) {
			return s.Index
		}
	}
	return -1
}

func subCodec(c string, external, forced bool) func(s mediaStream) bool {
	return func(s mediaStream) bool {
		return strings.EqualFold(s.Codec, c) && s.IsExternal == external && s.IsForced == forced
	}
}

func pickSub(f func(s mediaStream) bool) func(ms mediaSource) int {
	return func(ms mediaSource) int { return subIndex(ms, f) }
}

func hasFonts(ms mediaSource) bool { return len(ms.MediaAttachments) > 0 }

// secondAudio selects the second audio track (an audio-track switch).
func secondAudio(ms mediaSource) int {
	a := streamsOf(ms, "Audio")
	if len(a) < 2 {
		return -1
	}
	return a[1].Index
}

func commentaryIndex(ms mediaSource) int {
	for _, a := range streamsOf(ms, "Audio") {
		if strings.Contains(strings.ToLower(a.Title), "commentary") {
			return a.Index
		}
	}
	return -1
}

// Interaction helpers.

// resumeAt is a resume point off the 6 s segment grid (2:58 = 29 x 6 + 4),
// where the rc.8 audio dropouts began.
const resumeAt = 178

const (
	passFromZero        = passStream + " Play 10 min from 0."
	passResume          = "After the resume, picture and sound start together within 5 s at the saved position (within 2 s); then no audio dropout, A/V drift, stutter or green frame for 5 min."
	passSeek            = "Each seek lands within 2 s of the target and resumes picture and sound together within 3 s; then no audio dropout or A/V drift for 2 min."
	passResumeTranscode = "After the resume, picture and sound start together within 8 s at the saved position (within 2 s); then no stall, A/V drift or green frame for 5 min."
	passSeekTranscode   = "Each seek lands within 2 s of the target and resumes within 8 s; then no stall or A/V drift for 2 min."
)

// atLeast reports whether the item runs at least min minutes.
func atLeast(it item, min int) bool { return it.RunTimeTicks >= int64(min)*600_000_000 }

// mkv4K reports an MKV with 4K HEVC video of range type rt ("" for any).
func mkv4K(ms mediaSource, rt string) bool {
	return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) && (rt == "" || rangeIs(v, rt)) })
}

// is4KHDR reports 4K HEVC in HDR10, HDR10+ or Dolby Vision with an HDR10
// base layer.
func is4KHDR(ms mediaSource) bool {
	return vid(ms, "hevc", func(v mediaStream) bool {
		return is4K(v) && (rangeIs(v, "HDR10") || rangeIs(v, "DOVIWithHDR10") || rangeIs(v, "HDR10Plus"))
	})
}

// isCopyAudio reports an audio codec that the AVPlayer profiles copy.
func isCopyAudio(a mediaStream) bool {
	switch strings.ToLower(a.Codec) {
	case "aac", "ac3", "eac3":
		return true
	}
	return false
}

// copyThenTranscodeAudio selects a TrueHD or DTS track on a source whose
// first track the AVPlayer profiles copy: an audio switch that changes the
// delivery from a:copy to a:transcode.
func copyThenTranscodeAudio(ms mediaSource) int {
	a := streamsOf(ms, "Audio")
	if len(a) < 2 || !isCopyAudio(a[0]) || !containerIs(ms, "mkv") || !vid(ms, "hevc", nil) {
		return -1
	}
	for _, s := range a[1:] {
		if s.Codec == "truehd" || s.Codec == "dts" {
			return s.Index
		}
	}
	return -1
}

func expect(native, vlc, swiftfin Expect) map[string]Expect {
	return map[string]Expect{playerNative: native, playerVLC: vlc, playerSwiftfin: swiftfin}
}

func ex(method, audio, sub string) Expect { return Expect{Method: method, Audio: audio, Subtitle: sub} }

const (
	dp = "DirectPlay"
	rx = "Remux"
	tc = "Transcode"
	cp = "copy"
	at = "transcode"
)

const (
	passStream = "Starts in under 5 s; no audio dropout, A/V drift, stutter or green frames for the full play time; seeks land within 2 s of the target and resume audio and video together."
	passSubs   = passStream + " The selected subtitle shows with correct timing; turning it off removes it."
)

func byID(ids map[string]bool) func(it item, ms mediaSource) bool {
	return func(it item, _ mediaSource) bool { return ids[it.ID] }
}

// cells returns the matrix in report order.
func cells() []cell {
	out := []cell{
		// Regression cells.
		{
			ID: "ATV-R01", Group: "Regression", Title: "MKV HEVC Main10 SDR 23.976 AAC 2.0 + ASS with fonts",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "ASS with fonts", "ops": "10 min play, seeks"},
			Risk: "HLS fMP4 copy-remux relaunched ffmpeg per segment, so AVPlayer dropped audio for about 2 s at a time.",
			Pass: "No audio dropout over 10 min of play and after each seek. ASS styling and fonts render (SharkParty overlay).", Minutes: 10,
			pinned: true, subtitle: pickSub(subCodec("ass", false, false)),
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 10 && v.VideoRangeType == "SDR" && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2)) && hasFonts(ms) && hasSub(ms, subCodec("ass", false, false))
			},
			Expect: expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(tc, cp, "Encode")),
		},
		{
			ID: "ATV-R02", Group: "Regression", Title: "MKV HEVC Main 8-bit 23.976 AAC 2.0, short episode",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 8-bit", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "none", "ops": "full play, seeks"},
			Risk: "Second item that showed the audio dropouts; short episode, so the probe covers it end to end.",
			Pass: passStream, Minutes: 10, pinned: true,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 8 && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2)) && !hasSub(ms, func(mediaStream) bool { return true })
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		{
			ID: "ATV-R03", Group: "Regression", Title: "MKV HEVC Main10 SDR 4K scope EAC3 5.1, resumed at 2:58",
			Dims:    map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "fps": "23.976", "res": "4K", "audio": "EAC3 5.1", "subs": "none", "delivery": "Remux a:copy", "ops": "resume off-grid", "player": "SP-AV"},
			Risk:    "v0.3.0-rc.8: a resume at about 2:58 restarted ffmpeg with -ss/-start_number, the job cut segments on an hls_time grid from the restart point instead of the playlist's keyframe grid, and AVPlayer dropped audio for seconds at a time while video stayed smooth.",
			Pass:    passResume + " Resume from a saved position near 2:58 and play 10 min.",
			Minutes: 10, Entries: []float64{resumeAt}, pinned: true,
			match: func(_ item, ms mediaSource) bool {
				return mkv4K(ms, "SDR") && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 10 && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("eac3", 6))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		// Container x video codec.
		{
			ID: "ATV-V01", Group: "Video", Title: "MP4 H.264 High 1080p 23.976 AAC 2.0",
			Dims: map[string]string{"container": "MP4", "video": "H.264", "fps": "23.976", "res": "1080p", "audio": "AAC 2.0", "subs": "none"},
			Risk: "Baseline direct play for all three players.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" && is1080(v) && fpsNear(v, 23.976) }) &&
					onlyAudio(ms, audioCh("aac", 2))
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-V02", Group: "Video", Title: "MKV H.264 High 1080p AC3 5.1 + embedded SRT",
			Dims: map[string]string{"container": "MKV", "video": "H.264", "fps": "23.976", "res": "1080p", "audio": "AC3 5.1", "subs": "embedded SRT"},
			Risk: "The most common library shape: H.264 copy-remux into fMP4 with AC3 copy.", Pass: passSubs, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" && is1080(v) && v.BitDepth == 8 }) &&
					onlyAudio(ms, audioCh("ac3", 6)) && hasSub(ms, subCodec("subrip", false, false))
			},
			subtitle: pickSub(subCodec("subrip", false, false)),
			Expect:   expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(rx, cp, "Hls")),
		},
		{
			ID: "ATV-V03", Group: "Video", Title: "MP4 HEVC Main 8-bit 1080p AAC",
			Dims: map[string]string{"container": "MP4", "video": "HEVC 8-bit", "fps": "23.976", "res": "1080p", "audio": "AAC"},
			Risk: "AVPlayer requires codec tag hvc1/dvh1. Pinned upstream never fills CodecTag (MediaStreamInfo.cs:204 binds codec_tag_string?), so HEVC MP4 remuxes instead of direct play. Parity, not a defect.",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && vid(ms, "hevc", func(v mediaStream) bool { return v.BitDepth == 8 && is1080(v) }) && firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V04", Group: "Video", Title: "MKV HEVC Main10 SDR 720p 29.97 EAC3 5.1",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "fps": "29.97", "res": "720p", "audio": "EAC3 5.1"},
			Risk: "29.97 copy-remux: segment boundaries on a non-film cadence; EAC3 copy into fMP4.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool {
					return v.BitDepth == 10 && rangeIs(v, "SDR") && v.Height == 720 && fpsNear(v, 29.97)
				}) && firstAudio(ms, audioCh("eac3", 6))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V05", Group: "Video", Title: "MKV HEVC HDR10 4K TrueHD Atmos 7.1",
			Dims: map[string]string{"container": "MKV", "video": "HDR10", "fps": "23.976", "res": "4K", "audio": "TrueHD/Atmos 7.1"},
			Risk: "4K HDR10 copy-remux at high bitrate with an audio transcode from TrueHD; HDR mode switch on the TV.",
			Pass: passStream + " The TV switches to HDR10.", Human: "HDR mode switch; audio is a PCM/AAC/AC3 transcode, not Atmos.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10") && is4K(v) }) &&
					firstAudio(ms, func(a mediaStream) bool { return a.Codec == "truehd" && a.Channels == 8 })
			},
			Expect: expect(ex(rx, at, ""), ex(rx, at, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-V06", Group: "Video", Title: "MKV HEVC HDR10+ 4K",
			Dims: map[string]string{"container": "MKV", "video": "HDR10+", "res": "4K"},
			Risk: "HDR10+ dynamic metadata survives the copy-remux only if the SEI is copied; the TV shows HDR10+ or plain HDR10.",
			Pass: passStream, Human: "HDR mode on the TV: HDR10+ (or HDR10 on TVs without HDR10+).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10Plus") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V07", Group: "Video", Title: "Dolby Vision profile 5 (DOVI, no HDR10 fallback)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P5", "res": "4K"},
			Risk: "P5 has no HDR10 base layer: AVPlayer must see a dvh1 sample entry, and VLC (no DOVI in its range list) gets a transcode that needs tone mapping or shows purple/green.",
			Pass: passStream + " Colors are correct (no purple/green cast). The TV shows Dolby Vision on AVPlayer.", Human: "Dolby Vision mode switch and colors.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVI") })
			},
			Expect: expect(ex(rx, "", ""), ex(tc, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V08", Group: "Video", Title: "Dolby Vision profile 7 (DOVIWithEL)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P7", "res": "4K"},
			Risk: "Dual-layer UHD BD remux. AVPlayer plays the base layer; the enhancement layer is dropped.",
			Pass: passStream + " The TV shows HDR10 or Dolby Vision.", Human: "HDR/DV mode and colors.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVIWithEL") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V09", Group: "Video", Title: "Dolby Vision profile 8.1 (DOVIWithHDR10)",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P8", "res": "4K"},
			Risk: "The common streaming DV shape; the remux must keep the RPU and tag dvh1 so AVPlayer engages DV.",
			Pass: passStream + " The TV shows Dolby Vision.", Human: "Dolby Vision mode switch.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVIWithHDR10") })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V10", Group: "Video", Title: "MKV AV1 10-bit + Opus",
			Dims: map[string]string{"container": "MKV", "video": "AV1", "audio": "Opus"},
			Risk: "Apple TV 4K has no AV1 decode: a full video transcode (QSV on live). Opus is copied for VLC only.",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "av1", func(v mediaStream) bool { return v.BitDepth == 10 }) && firstAudio(ms, codec("opus"))
			},
			Expect: expect(ex(tc, at, ""), ex(tc, cp, ""), ex(tc, at, "")),
		},
		{
			ID: "ATV-V11", Group: "Video", Title: "VC-1 (Blu-ray remux) with DTS or AC3",
			Dims: map[string]string{"video": "VC-1"},
			Risk: "Legacy codec: AVPlayer needs a full transcode; VLC plays it directly. With TrueHD first, VLC also transcodes the video, because HLS cannot carry VC-1 (StreamBuilder.cs:31 _supportedHlsVideoCodecs).",
			Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "vc1", nil) && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" || a.Codec == "ac3" })
			},
			Expect: expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V12", Group: "Video", Title: "MPEG-2 video (DVD or broadcast)",
			Dims: map[string]string{"container": "TS/MPEG-PS", "video": "MPEG-2", "res": "SD"},
			Risk: "MPEG-2 in a program or transport stream; AVPlayer needs a transcode, often with deinterlace.", Pass: passStream, Minutes: 3,
			match:   func(_ item, ms mediaSource) bool { return vid(ms, "mpeg2video", nil) },
			Expect:  expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=720x480:rate=30000/1001 -f lavfi -i sine=f=440:sample_rate=48000 -t 900 -c:v mpeg2video -b:v 6M -flags +ildct+ilme -c:a ac3 -b:a 192k -f vob sample-mpeg2.vob",
		},
		{
			ID: "ATV-V13", Group: "Video", Title: "MKV H.264 High 10 (Hi10P anime) + FLAC",
			Dims: map[string]string{"container": "MKV", "video": "H.264 10-bit", "audio": "FLAC"},
			Risk: "No Apple decoder for H.264 10-bit, so both players transcode the video; FLAC audio.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" }) && firstAudio(ms, codec("flac"))
			},
			Expect: expect(ex(tc, cp, ""), ex(tc, cp, ""), ex(tc, cp, "")),
		},
		{
			ID: "ATV-V14", Group: "Video", Title: "MPEG-TS H.264 progressive + AAC",
			Dims: map[string]string{"container": "TS/M2TS", "video": "H.264", "audio": "AAC"},
			Risk: "TS source timestamps (non-zero start, PCR wrap) through an fMP4 remux.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "ts", "m2ts", "mpegts") && vid(ms, "h264", func(v mediaStream) bool { return !v.IsInterlaced }) &&
					firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-V15", Group: "Video", Title: "AVI MPEG-4 ASP (XviD) + MP3",
			Dims: map[string]string{"container": "AVI", "video": "MPEG-4 ASP", "audio": "MP3", "res": "SD"},
			Risk: "Legacy AVI: packed B-frames and VFR-like timestamps; HLS cannot carry MPEG-4 ASP, so AVPlayer transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "avi") && vid(ms, "mpeg4", nil) && firstAudio(ms, codec("mp3"))
			},
			Expect: expect(ex(tc, at, ""), ex(dp, cp, ""), ex(tc, at, "")),
		},
		{
			ID: "ATV-V16", Group: "Video", Title: "HEVC Range Extensions (Rext, 4:2:2/4:4:4)",
			Dims: map[string]string{"video": "HEVC Rext"},
			Risk: "Profile outside main/main 10: every player transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Rext" })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V17", Group: "Video", Title: "4K HEVC 10-bit SDR",
			Dims: map[string]string{"video": "HEVC 10-bit SDR", "res": "4K"},
			Risk: "4K SDR copy-remux: high segment sizes and no HDR mode switch.", Pass: passStream + " The TV stays in SDR.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) && rangeIs(v, "SDR") && v.BitDepth == 10 })
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-V18", Group: "Video", Title: "SD H.264 25 fps (PAL)",
			Dims: map[string]string{"video": "H.264", "fps": "25", "res": "SD"},
			Risk: "PAL cadence and SD scaling; display frame-rate match on the TV.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return isSD(v) && fpsNear(v, 25) && !v.IsInterlaced })
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V19", Group: "Video", Title: "24.000 fps 1080p",
			Dims: map[string]string{"fps": "24", "res": "1080p"},
			Risk: "Exact 24p against the 23.976 majority; match-frame-rate on the TV.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && fpsNear(*v, 24) && is1080(*v) && (v.Codec == "h264" || v.Codec == "hevc")
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V20", Group: "Video", Title: "50 fps",
			Dims: map[string]string{"fps": "50"},
			Risk: "PAL broadcast/sport cadence.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && fpsNear(*v, 50)
			},
			Expect:  expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=1920x1080:rate=50 -f lavfi -i sine=f=440:sample_rate=48000 -t 900 -c:v libx264 -profile:v high -pix_fmt yuv420p -c:a aac -b:a 160k sample-h264-50p.mkv",
		},
		{
			ID: "ATV-V21", Group: "Video", Title: "59.94 fps",
			Dims: map[string]string{"fps": "59.94"},
			Risk: "High frame rate: HEVC is capped at 60 fps in the AVPlayer profile; decode load and frame drops.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && (fpsNear(*v, 59.94) || fpsNear(*v, 60))
			},
			Expect: expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-V22", Group: "Video", Title: "Variable frame rate (RealFrameRate differs from AverageFrameRate)",
			Dims: map[string]string{"fps": "VFR"},
			Risk: "VFR sources break fixed-length segment math and A/V sync in a copy-remux.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && !v.IsAnamorphic && v.AverageFrameRate > 1 && math.Abs(v.RealFrameRate-v.AverageFrameRate) > 1
			},
			Expect:  expect(ex("", "", ""), ex(dp, "", ""), ex("", "", "")),
			Missing: "Use a phone screen recording (VFR H.264 in MP4).",
		},
		{
			ID: "ATV-V24", Group: "Video", Title: "Anamorphic SD H.264 (DVD rip)",
			Dims: map[string]string{"video": "H.264 anamorphic", "res": "SD"},
			Risk: "Every tvOS profile rejects anamorphic H.264, so all players transcode to square pixels; the aspect ratio must survive.", Pass: passStream + " The picture is not stretched or squeezed.",
			Human: "Aspect ratio.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.IsAnamorphic && isSD(v) })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-V23", Group: "Video", Title: "Interlaced H.264",
			Dims: map[string]string{"video": "H.264 interlaced"},
			Risk: "AVPlayer profile rejects interlaced H.264: a deinterlacing transcode (QSV deinterlace on live).", Pass: passStream + " No combing.",
			Human: "Bandwidth: the mapped item declares 28.57 Mbit/s in the master playlist, but segments peak at 38.1 Mbit/s, so macOS AVPlayer logs -12318 (upstream parity). On the TV, watch for stalls or quality drops during play and after a seek.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.IsInterlaced })
			},
			Expect: expect(ex(tc, "", ""), ex(dp, "", ""), ex(tc, "", "")),
		},

		// Audio.
		{
			ID: "ATV-A01", Group: "Audio", Title: "DTS 5.1 core",
			Dims: map[string]string{"audio": "DTS 5.1"},
			Risk: "AVPlayer cannot decode DTS: audio transcode in a video-copy remux.", Pass: passStream + " Audio is 5.1 at the receiver.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return onlyAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" && a.Profile == "DTS" && a.Channels == 6 }) &&
					vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" })
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-A02", Group: "Audio", Title: "DTS-HD MA 7.1",
			Dims: map[string]string{"audio": "DTS-HD MA 7.1"},
			Risk: "8-channel transcode: channel layout and the AAC 7.1 path into AVPlayer.", Pass: passStream + " 7.1 channels map correctly.", Human: "Channel mapping (rear vs side surrounds).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" && a.Profile == "DTS-HD MA" && a.Channels == 8 })
			},
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A03", Group: "Audio", Title: "TrueHD Atmos below 4K",
			Dims: map[string]string{"audio": "TrueHD/Atmos"},
			Risk: "TrueHD is in no tvOS profile: every player transcodes audio; Atmos is lost.", Pass: passStream, Human: "Receiver shows multichannel PCM/AC3, not Atmos (expected).", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "truehd" && strings.Contains(a.Profile, "Atmos") }) &&
					!vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) })
			},
			Expect: expect(ex("", at, ""), ex("", at, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A04", Group: "Audio", Title: "EAC3 Atmos (DD+ JOC)",
			Dims: map[string]string{"audio": "EAC3/Atmos"},
			Risk: "EAC3 copy must keep the JOC extension so the receiver shows Atmos.", Pass: passStream, Human: "Receiver shows Dolby Atmos.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "eac3" && strings.Contains(a.Profile, "Atmos") })
			},
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A05", Group: "Audio", Title: "FLAC 2.0 in MKV",
			Dims: map[string]string{"audio": "FLAC 2.0"},
			Risk: "FLAC copied into fMP4 HLS: AVPlayer support for fLaC in HLS is thin.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && onlyAudio(ms, audioCh("flac", 2)) &&
					vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Main 10" || v.Profile == "Main" })
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-A06", Group: "Audio", Title: "Opus 2.0 in MKV",
			Dims: map[string]string{"audio": "Opus 2.0"},
			Risk: "Opus is not in the AVPlayer profile: audio transcode beside a video copy.", Pass: passStream, Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && onlyAudio(ms, audioCh("opus", 2)) &&
					vid(ms, "hevc", func(v mediaStream) bool { return v.Profile == "Main" || v.Profile == "Main 10" })
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-A07", Group: "Audio", Title: "Opus 5.1 in MKV",
			Dims: map[string]string{"audio": "Opus 5.1"},
			Risk: "Opus multichannel mapping (Vorbis order) into a 5.1 transcode.", Pass: passStream + " Centre dialogue stays in the centre.", Human: "Channel mapping.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return containerIs(ms, "mkv") && firstAudio(ms, audioCh("opus", 6)) },
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},
		{
			ID: "ATV-A08", Group: "Audio", Title: "MP3 2.0 in MKV",
			Dims: map[string]string{"audio": "MP3"},
			Risk: "MP3 is direct-playable only in MOV for AVPlayer; in MKV it transcodes.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && firstAudio(ms, codec("mp3")) && !vid(ms, "h264", func(v mediaStream) bool { return v.IsAnamorphic })
			},
			Expect:  expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
			Missing: "ffmpeg -f lavfi -i testsrc2=size=1280x720:rate=24000/1001 -f lavfi -i sine=f=440:sample_rate=44100 -t 900 -c:v libx264 -c:a libmp3lame -b:a 192k sample-mp3.mkv",
		},
		{
			ID: "ATV-A09", Group: "Audio", Title: "AC3 mono",
			Dims: map[string]string{"audio": "AC3 1.0"},
			Risk: "Mono must play from the centre (or both fronts), not one side.", Pass: passStream, Human: "Mono plays centred.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return firstAudio(ms, audioCh("ac3", 1)) },
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A10", Group: "Audio", Title: "MP4 AAC 5.1",
			Dims: map[string]string{"container": "MP4", "audio": "AAC 5.1"},
			Risk: "Multichannel AAC direct play.", Pass: passStream, Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return isMP4(ms) && onlyAudio(ms, audioCh("aac", 6)) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" || v.Profile == "Main" })
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-A11", Group: "Audio", Title: "HE-AAC (SBR, v2 where the library has it)",
			Dims: map[string]string{"audio": "HE-AACv2"},
			Risk: "Parametric stereo copied into fMP4; a wrong ASC plays mono or at half speed.", Pass: passStream + " Audio is stereo at the right pitch.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return len(a) > 0 && a[0].Codec == "aac" && (a[0].Profile == "HE-AACv2" || a[0].Profile == "HE-AAC")
			},
			Expect: expect(ex("", cp, ""), ex(dp, cp, ""), ex("", cp, "")),
		},
		{
			ID: "ATV-A12", Group: "Audio", Title: "Dual audio: switch to the second track",
			Dims: map[string]string{"audio": "multiple tracks", "ops": "audio-track switch"},
			Risk: "Swiftfin restarts playback with a new AudioStreamIndex; the remux must map the selected track.", Pass: passStream + " The selected language plays.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return containerIs(ms, "mkv") && len(a) == 2 && a[0].Language != a[1].Language && a[0].Codec == a[1].Codec &&
					(a[0].Codec == "aac" || a[0].Codec == "ac3" || a[0].Codec == "eac3") && vid(ms, "hevc", nil)
			},
			audio:  secondAudio,
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-A13", Group: "Audio", Title: "Commentary track selected",
			Dims: map[string]string{"audio": "commentary"},
			Risk: "A non-default secondary track; mapping by index, not by default flag.", Pass: passStream + " The commentary plays.", Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return commentaryIndex(ms) >= 0 },
			audio:  commentaryIndex,
			Expect: expect(ex("", "", ""), ex("", "", ""), ex("", "", "")),
		},
		{
			ID: "ATV-A14", Group: "Audio", Title: "PCM s16le in MKV",
			Dims: map[string]string{"audio": "PCM"},
			Risk: "PCM is direct-playable only from MOV for AVPlayer.", Pass: passStream, Minutes: 3,
			match:  func(_ item, ms mediaSource) bool { return containerIs(ms, "mkv") && firstAudio(ms, codec("pcm_s16le")) },
			Expect: expect(ex("", at, ""), ex(dp, cp, ""), ex("", at, "")),
		},

		// Subtitles.
		{
			ID: "ATV-S01", Group: "Subtitles", Title: "External SRT sidecar",
			Dims: map[string]string{"subs": "external SRT"},
			Risk: "Sidecar delivery and the subtitle switch/off path.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("subrip", true, false)) },
			subtitle: pickSub(subCodec("subrip", true, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "External"), ex("", "", "")),
		},
		{
			ID: "ATV-S02", Group: "Subtitles", Title: "Embedded forced SRT",
			Dims: map[string]string{"subs": "forced"},
			Risk: "Forced tracks must show without a selection (client) and deliver like any SRT (server).", Pass: passSubs, Human: "Forced subtitles appear without selecting a track.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && hasSub(ms, subCodec("subrip", false, true))
			},
			subtitle: pickSub(subCodec("subrip", false, true)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex("", "", "Hls")),
		},
		{
			ID: "ATV-S03", Group: "Subtitles", Title: "ASS with embedded fonts (styled anime)",
			Dims: map[string]string{"subs": "ASS with fonts"},
			Risk: "SharkParty overlay draws ASS with MKV fonts; stock Swiftfin burns it in (ASS cannot convert to WebVTT upstream).", Pass: passSubs + " Styling, positioning and fonts match the source.",
			Human: "Subtitle styling (fonts, karaoke, signs).", Minutes: 3,
			match: func(it item, ms mediaSource) bool {
				return hasFonts(ms) && hasSub(ms, subCodec("ass", false, false)) && vid(ms, "h264", nil)
			},
			subtitle: pickSub(subCodec("ass", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S04", Group: "Subtitles", Title: "PGS (Blu-ray image subtitles)",
			Dims: map[string]string{"subs": "PGS"},
			Risk: "SharkParty gets the .sup as External; stock Swiftfin burns PGS in, a full transcode.", Pass: passSubs, Human: "PGS position and size on 4K. On stock Swiftfin (an iGPU limit): the seek segment takes 6.3-6.7 s and the transcode runs at about 1.5x realtime. Confirm that play recovers after a seek and does not stall.", Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("PGSSUB", false, false)) },
			subtitle: pickSub(subCodec("PGSSUB", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S05", Group: "Subtitles", Title: "VobSub (DVD image subtitles)",
			Dims: map[string]string{"subs": "VobSub"},
			Risk: "AVPlayer profiles burn DVD subtitles in: a full transcode with overlay.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("DVDSUB", false, false)) },
			subtitle: pickSub(subCodec("DVDSUB", false, false)),
			Expect:   expect(ex(tc, "", "Encode"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S06", Group: "Subtitles", Title: "Forced ASS",
			Dims: map[string]string{"subs": "forced ASS"},
			Risk: "Signs-and-songs tracks are forced ASS; same delivery as ASS.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return hasSub(ms, subCodec("ass", false, true)) },
			subtitle: pickSub(subCodec("ass", false, true)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-S07", Group: "Subtitles", Title: "MP4 mov_text",
			Dims: map[string]string{"container": "MP4", "subs": "mov_text"},
			Risk: "mov_text is in no AVPlayer profile: SharkParty converts it to External SRT; stock Swiftfin has no direct-play text path, so it goes to HLS with WebVTT in the manifest.", Pass: passSubs, Minutes: 3,
			match:    func(_ item, ms mediaSource) bool { return isMP4(ms) && hasSub(ms, subCodec("mov_text", false, false)) },
			subtitle: pickSub(subCodec("mov_text", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex("", "", "Hls")),
		},
		{
			ID: "ATV-S08", Group: "Subtitles", Title: "Subtitles off on a file with many tracks",
			Dims: map[string]string{"subs": "none (off)", "ops": "subtitle switch and off"},
			Risk: "Off must not burn in or embed anything.", Pass: passStream + " No subtitle shows.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && len(streamsOf(ms, "Subtitle")) >= 5 && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" }) &&
					firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		// Operations that need a series context or the TV itself.
		{
			ID: "ATV-O01", Group: "Operations", Title: "Next-episode autoplay across a remux",
			Dims: map[string]string{"ops": "next-episode autoplay, resume from position"},
			Risk: "End-of-stream on HLS, the next PlaybackInfo, and the resume point of the next episode.", Pass: "The next episode starts by itself; stopping mid-episode and starting again resumes within 10 s of the stop point.",
			Human: "Autoplay countdown and resume.", Minutes: 2,
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && containerIs(ms, "mkv") && vid(ms, "h264", nil) && onlyAudio(ms, audioCh("aac", 2)) && noSubs(ms)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-O02", Group: "Operations", Title: "Background and foreground during a transcode",
			Dims: map[string]string{"ops": "background/foreground, pause/resume"},
			Risk: "The server kills an idle HLS job after 60 s; returning from the home screen must restart cleanly.", Pass: "After 2 min on the home screen, the app resumes at the same position.",
			Human: "Background for 2 min, then return.", Minutes: 2,
			match:  func(_ item, ms mediaSource) bool { return vid(ms, "av1", nil) },
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-O03", Group: "Operations", Title: "HDR/SDR mode switch between items",
			Dims: map[string]string{"ops": "HDR mode switch"},
			Risk: "Going from an HDR10 item to an SDR item must return the TV to SDR.", Pass: "The TV switches to HDR10 and back to SDR without a blank screen over 5 s.",
			Human: "TV mode switch both ways.", Minutes: 1,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "HDR10") && is4K(v) }) && firstAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-O04", Group: "Operations", Title: "Scrub with trickplay on a 4K remux",
			Dims: map[string]string{"ops": "scrub"},
			Risk: "Long scrubs on a remux restart ffmpeg at a far segment; trickplay thumbnails load.", Pass: "Thumbnails show while scrubbing; playback resumes within 3 s of release.",
			Human: "Scrub feel and thumbnails.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return is4K(v) && rangeIs(v, "SDR") }) && firstAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},

		// Interaction cells: what the viewer does x how the server delivers x
		// the media traits that change the outcome. See "Interaction cells"
		// in docs/qa/atv-matrix.md for the dimensions and the pruning.

		// Remux, audio copy: the SharkParty AVPlayer path for most 4K files.
		{
			ID: "ATV-I01", Group: "Interaction", Title: "Remux a:copy, start at 0: 4K SDR 10-bit + EAC3 5.1 (control for R03)",
			Dims: map[string]string{"container": "MKV", "video": "HEVC 10-bit SDR", "res": "4K", "audio": "EAC3 5.1", "delivery": "Remux a:copy", "ops": "start at 0", "player": "SP-AV"},
			Risk: "The same media as ATV-R03, started from 0: a pass here and a fail in R03 isolates the defect to the restart, not to the media.",
			Pass: passFromZero, Minutes: 10,
			match: func(it item, ms mediaSource) bool {
				return mkv4K(ms, "SDR") && firstAudio(ms, audioCh("eac3", 6)) && atLeast(it, 90)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I02", Group: "Interaction", Title: "Remux a:copy, resume off the 6 s grid: 1080p HEVC + EAC3/AC3",
			Dims: map[string]string{"container": "MKV", "video": "HEVC", "res": "1080p", "audio": "EAC3/AC3", "delivery": "Remux a:copy", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "A resume restarts ffmpeg with -ss and -start_number, and the restarted job cuts segments from the restart point, not on the playlist's keyframe grid. 1080p separates the restart defect from 4K bitrate.",
			Pass: passResume, Minutes: 10, Entries: []float64{resumeAt},
			match: func(it item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", is1080) && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "eac3" || a.Codec == "ac3" }) && atLeast(it, 40)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I03", Group: "Interaction", Title: "Remux a:copy, resume: Dolby Vision 8.1 + EAC3 Atmos",
			Dims: map[string]string{"container": "MKV", "video": "Dolby Vision P8", "res": "4K", "audio": "EAC3/Atmos", "delivery": "Remux a:copy", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "High-risk triple. The restarted job must start on an IDR frame with its RPU, keep the dvh1 sample entry of the first job, and keep the JOC extension in the first EAC3 frame after the cut.",
			Pass: passResume + " The TV shows Dolby Vision; the receiver shows Atmos after the resume.", Human: "DV and Atmos indicators after the resume.", Minutes: 10, Entries: []float64{resumeAt},
			match: func(it item, ms mediaSource) bool {
				return mkv4K(ms, "DOVIWithHDR10") && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "eac3" && strings.Contains(a.Profile, "Atmos") })
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I04", Group: "Interaction", Title: "Remux a:copy, seek forward far: 4K HDR, runtime over 150 min",
			Dims: map[string]string{"res": "4K", "video": "HDR10/DV", "audio": "EAC3", "delivery": "Remux a:copy", "ops": "seek forward far", "player": "SP-AV", "runtime": "over 150 min"},
			Risk: "A jump of more than 60 min into a large 4K file: a high -start_number, a long input seek, and a segment index far from the running job, which the server must kill and replace.",
			Pass: passSeek, Minutes: 5, Entries: []float64{5400},
			match: func(it item, ms mediaSource) bool {
				return is4KHDR(ms) && containerIs(ms, "mkv") && firstAudio(ms, codec("eac3")) && atLeast(it, 150)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I05", Group: "Interaction", Title: "Remux a:copy, seek backward behind the job: 4K HEVC",
			Dims: map[string]string{"res": "4K", "video": "HEVC", "delivery": "Remux a:copy", "ops": "seek backward", "player": "SP-AV"},
			Risk: "After a forward seek, a seek back to segments the current job never made needs a second restart; old and new jobs must not both write the same segment numbers.",
			Pass: passSeek + " Seek forward 10 min, then back 15 min: both land and play cleanly.", Minutes: 5, Entries: []float64{1200, 300},
			match: func(it item, ms mediaSource) bool {
				return mkv4K(ms, "") && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "eac3" || a.Codec == "ac3" }) && atLeast(it, 60)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I06", Group: "Interaction", Title: "Remux a:copy, seek to the last 30 s: MKV HEVC episode",
			Dims: map[string]string{"container": "MKV", "video": "HEVC", "delivery": "Remux a:copy", "ops": "seek near end", "player": "SP-AV"},
			Risk: "The restart seek clamps to runtime - 5 s (#482); the last segment is short, and end of stream must reach the client as an ENDLIST, not a stall.",
			Pass: "The seek lands within 2 s; the last seconds play with sound; the player ends the item (or shows next-up) without a spinner.", Minutes: 2, Entries: []float64{-30},
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && containerIs(ms, "mkv") && vid(ms, "hevc", nil) && firstAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I07", Group: "Interaction", Title: "Remux a:copy, audio-track switch mid-play: two AAC/AC3/EAC3 tracks",
			Dims: map[string]string{"container": "MKV", "audio": "multiple tracks", "delivery": "Remux a:copy", "ops": "audio-track switch mid-play", "player": "SP-AV"},
			Risk: "Swiftfin restarts playback with a new AudioStreamIndex at the current position: a resume off the grid with a different -map. The new job must not reuse the old job's segments.",
			Pass: passResume + " Switch at about 5 min: the new language plays from the switch point, with no echo of the old track.", Minutes: 5, Entries: []float64{300},
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return containerIs(ms, "mkv") && vid(ms, "hevc", nil) && len(a) >= 2 && a[0].Language != a[1].Language &&
					isCopyAudio(a[0]) && isCopyAudio(a[1])
			},
			audio:  secondAudio,
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I08", Group: "Interaction", Title: "Remux a:copy, pause over 5 min then resume: 4K",
			Dims: map[string]string{"res": "4K", "delivery": "Remux a:copy", "ops": "long pause", "player": "SP-AV"},
			Risk: "The server reaps the idle HLS job; on play, AVPlayer asks for the next segment and the server restarts at that segment: the resume path without a new PlaybackInfo.",
			Pass: passResume + " Pause at about 3 min for 6 min, then play.", Human: "Pause for over 5 min.", Minutes: 3, Entries: []float64{resumeAt},
			match: func(it item, ms mediaSource) bool {
				return mkv4K(ms, "HDR10") && firstAudio(ms, codec("eac3")) && atLeast(it, 30)
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I09", Group: "Interaction", Title: "Remux a:copy, background and foreground: Dolby Vision 8.1",
			Dims: map[string]string{"video": "Dolby Vision P8", "res": "4K", "delivery": "Remux a:copy", "ops": "background/foreground", "player": "SP-AV"},
			Risk: "tvOS tears down the player in the background; on return the app rebuilds the item at the saved position, so the TV leaves and re-enters DV around a restarted job.",
			Pass: "After 2 min on the home screen, the app resumes within 10 s of the position, in Dolby Vision, with sound.", Human: "Home button for 2 min, then return.", Minutes: 3, Entries: []float64{resumeAt},
			match:  func(_ item, ms mediaSource) bool { return mkv4K(ms, "DOVIWithHDR10") && firstAudio(ms, codec("eac3")) },
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I10", Group: "Interaction", Title: "Remux a:copy, next-episode autoplay: HEVC + EAC3 series",
			Dims: map[string]string{"video": "HEVC", "audio": "EAC3", "delivery": "Remux a:copy", "ops": "next-episode autoplay", "player": "SP-AV"},
			Risk: "The old job must stop and the new one start from 0 with a new play session; a stale job or a reused transcode directory gives the next episode the old media.",
			Pass: "Seek to the last minute; the next episode starts by itself from 0 with sound, and its first minutes play cleanly.", Human: "Autoplay to the next episode.", Minutes: 2, Entries: []float64{-60},
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && containerIs(ms, "mkv") && vid(ms, "hevc", nil) && onlyAudio(ms, codec("eac3"))
			},
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I11", Group: "Interaction", Title: "Two concurrent 4K remux streams (two devices)",
			Dims: map[string]string{"res": "4K", "delivery": "Remux a:copy", "ops": "two concurrent streams", "player": "SP-AV"},
			Risk: "Two jobs share the transcode directory, disk and the job table (#514 had gaps and 500s). One stream's seek must not kill or poison the other.",
			Pass: "Play this item on the Apple TV and a second 4K item (any I-cell) on another client; seek on one: the other plays on without a stall.", Human: "Needs a second client.", Minutes: 5,
			match:  func(_ item, ms mediaSource) bool { return mkv4K(ms, "HDR10") && firstAudio(ms, codec("eac3")) },
			Expect: expect(ex(rx, cp, ""), ex(dp, cp, ""), ex(rx, cp, "")),
		},
		{
			ID: "ATV-I12", Group: "Interaction", Title: "Remux a:copy, resume: variable frame rate",
			Dims: map[string]string{"fps": "VFR", "delivery": "Remux a:copy", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "On VFR video, segment math from a frame rate is wrong, so a restart lands farther from the playlist grid than on CFR.",
			Pass: passResume, Minutes: 5, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				v := videoOf(ms)
				return v != nil && (v.Codec == "hevc" || v.Codec == "h264") && v.Profile != "High 10" && !v.IsInterlaced && !v.IsAnamorphic &&
					v.AverageFrameRate > 1 && math.Abs(v.RealFrameRate-v.AverageFrameRate) > 1
			},
			Expect: expect(ex(rx, "", ""), ex(dp, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-I13", Group: "Interaction", Title: "Remux a:copy + PGS on, resume: 4K",
			Dims: map[string]string{"res": "4K", "subs": "PGS", "delivery": "Remux a:copy", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "The SharkParty overlay reads the external .sup and must place cues at the resumed media time, not at the restart's zero.",
			Pass: passResume + " PGS cues appear on time after the resume.", Human: "Subtitle timing after a resume.", Minutes: 5, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				return mkv4K(ms, "") && firstAudio(ms, isCopyAudio) && hasSub(ms, subCodec("PGSSUB", false, false))
			},
			subtitle: pickSub(subCodec("PGSSUB", false, false)),
			Expect:   expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-I14", Group: "Interaction", Title: "Remux a:copy, subtitle switch mid-play: text to image to off",
			Dims: map[string]string{"subs": "SRT + PGS", "delivery": "Remux a:copy", "ops": "subtitle on/off/switch", "player": "SP-AV"},
			Risk: "On SharkParty the switch is client-side (External), so the stream must not restart; a restart here means the client sent a new PlaybackInfo.",
			Pass: passSubs + " Switch SRT to PGS to off: each change applies within 2 s, with no rebuffer.", Human: "Subtitle menu switches.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", nil) && firstAudio(ms, isCopyAudio) &&
					hasSub(ms, subCodec("subrip", false, false)) && hasSub(ms, subCodec("PGSSUB", false, false))
			},
			subtitle: pickSub(subCodec("subrip", false, false)),
			Expect:   expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(rx, cp, "Hls")),
		},

		// Remux, audio transcode: TrueHD, DTS and Opus beside a video copy.
		{
			ID: "ATV-I15", Group: "Interaction", Title: "Remux a:transcode, resume: 4K HDR + TrueHD Atmos",
			Dims: map[string]string{"res": "4K", "video": "HDR10/DV", "audio": "TrueHD/Atmos", "delivery": "Remux a:transcode", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "High-risk triple. Two clocks restart together: the copied video cuts at a keyframe, the encoded audio starts with encoder priming. Any offset between them is a gap at every segment edge.",
			Pass: passResume, Minutes: 10, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				return is4KHDR(ms) && containerIs(ms, "mkv") && firstAudio(ms, func(a mediaStream) bool { return a.Codec == "truehd" })
			},
			Expect: expect(ex(rx, at, ""), ex(rx, at, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I16", Group: "Interaction", Title: "Remux a:transcode, seek forward far: DTS-HD MA, long runtime",
			Dims: map[string]string{"audio": "DTS-HD MA", "delivery": "Remux a:transcode", "ops": "seek forward far", "player": "SP-AV", "runtime": "over 120 min"},
			Risk: "A long input seek with a DTS decoder restart; the first audio frames after -ss can be dropped, so audio starts late against video.",
			Pass: passSeek, Minutes: 5, Entries: []float64{4800},
			match: func(it item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" && strings.HasPrefix(a.Profile, "DTS-HD MA") }) &&
					vid(ms, "hevc", nil) && atLeast(it, 120)
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I17", Group: "Interaction", Title: "Remux a:transcode, seek backward: DTS 5.1",
			Dims: map[string]string{"audio": "DTS", "delivery": "Remux a:transcode", "ops": "seek backward", "player": "SP-AV"},
			Risk: "A backward seek restarts both the copy and the audio encoder on segments the client already played; AVPlayer must not mix cached and new segments.",
			Pass: passSeek, Minutes: 5, Entries: []float64{900, 300},
			match: func(it item, ms mediaSource) bool {
				return firstAudio(ms, func(a mediaStream) bool { return a.Codec == "dts" }) && containerIs(ms, "mkv") &&
					(vid(ms, "hevc", nil) || vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" })) && atLeast(it, 40)
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I18", Group: "Interaction", Title: "Audio switch from a copy track to a transcode track: EAC3/AC3 to TrueHD/DTS",
			Dims: map[string]string{"audio": "multiple tracks", "delivery": "Remux a:copy to a:transcode", "ops": "audio-track switch mid-play", "player": "SP-AV"},
			Risk: "The delivery changes mid-play: the new job transcodes audio where the old copied it, at an off-grid position, and the audio codec in the init segment changes.",
			Pass: passResume + " Switch at about 5 min: the new track plays from the switch point.", Minutes: 5, Entries: []float64{300},
			match:  func(_ item, ms mediaSource) bool { return copyThenTranscodeAudio(ms) >= 0 },
			audio:  copyThenTranscodeAudio,
			Expect: expect(ex(rx, at, ""), ex("", "", ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I19", Group: "Interaction", Title: "Remux a:transcode, pause over 5 min and background: Opus or DTS",
			Dims: map[string]string{"audio": "Opus/DTS", "delivery": "Remux a:transcode", "ops": "long pause, background/foreground", "player": "SP-AV"},
			Risk: "The reaped job restarts the audio encoder at the paused segment; a stale encoder or a reused segment gives a gap at the resume point.",
			Pass: passResume + " Pause 6 min, play 1 min, then 2 min on the home screen and return.", Human: "Pause and background.", Minutes: 3, Entries: []float64{resumeAt},
			match: func(it item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", nil) && atLeast(it, 20) &&
					firstAudio(ms, func(a mediaStream) bool { return a.Codec == "opus" || a.Codec == "dts" })
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I20", Group: "Interaction", Title: "Remux a:transcode, seek to the last 30 s: TrueHD",
			Dims: map[string]string{"audio": "TrueHD", "delivery": "Remux a:transcode", "ops": "seek near end", "player": "SP-AV"},
			Risk: "The audio encoder flushes at end of input; a short last segment or a missing final audio packet stalls AVPlayer before the end.",
			Pass: "The seek lands within 2 s; sound plays to the end; the item ends without a spinner.", Minutes: 2, Entries: []float64{-30},
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", nil) && firstAudio(ms, codec("truehd"))
			},
			Expect: expect(ex(rx, at, ""), ex(rx, at, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I21", Group: "Interaction", Title: "Remux a:transcode, next-episode autoplay: Opus or FLAC series",
			Dims: map[string]string{"audio": "Opus/FLAC", "delivery": "Remux a:transcode", "ops": "next-episode autoplay", "player": "SP-AV"},
			Risk: "Episode-to-episode job handover while an audio encoder runs; the new episode must start from 0 with its own audio.",
			Pass: "Seek to the last minute; the next episode starts by itself from 0 with sound.", Human: "Autoplay to the next episode.", Minutes: 2, Entries: []float64{-60},
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && containerIs(ms, "mkv") && vid(ms, "hevc", nil) && firstAudio(ms, codec("opus"))
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I22", Group: "Interaction", Title: "Text subtitle switch on stock Swiftfin: HLS WebVTT, DTS",
			Dims: map[string]string{"audio": "DTS", "subs": "two SRT tracks", "delivery": "Remux a:transcode", "ops": "subtitle on/off/switch", "player": "SF-AV"},
			Risk: "Stock Swiftfin carries text subtitles as WebVTT in the manifest; a switch sends a new PlaybackInfo, a new job at the current position, and a new subtitle playlist that must line up with the restarted media.",
			Pass: passSubs + " Switch to the second track at about 5 min: cues stay in sync.", Human: "Stock Swiftfin build.", Minutes: 5, Entries: []float64{300},
			match: func(_ item, ms mediaSource) bool {
				n := 0
				for _, s := range streamsOf(ms, "Subtitle") {
					if strings.EqualFold(s.Codec, "subrip") && !s.IsExternal {
						n++
					}
				}
				return n >= 2 && containerIs(ms, "mkv") && vid(ms, "hevc", nil) && firstAudio(ms, codec("dts"))
			},
			subtitle: pickSub(subCodec("subrip", false, false)),
			Expect:   expect(ex(rx, at, "External"), ex(dp, cp, "Embed"), ex(rx, at, "Hls")),
		},

		// Video transcode, SDR output.
		{
			ID: "ATV-I23", Group: "Interaction", Title: "Transcode, resume: H.264 Hi10P anime",
			Dims: map[string]string{"video": "H.264 10-bit", "delivery": "Transcode SDR", "ops": "resume off-grid", "player": "SP-AV"},
			Risk: "A transcode forces keyframes on the segment grid, so the grid holds; the risk is restart latency (6-8 s for the first segment at 1080p QSV) and the encoder's first GOP after -ss.",
			Pass: passResumeTranscode, Minutes: 5, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-I24", Group: "Interaction", Title: "Transcode, seek forward far then to the end: AV1, VC-1 or MPEG-2",
			Dims: map[string]string{"video": "AV1/VC-1/MPEG-2", "delivery": "Transcode SDR", "ops": "seek forward far, seek near end", "player": "SP-AV"},
			Risk: "Each far seek restarts the encoder; near the end the encoder must flush its last GOP so the final segment exists.",
			Pass: passSeekTranscode + " A seek to the last 30 s plays to the end.", Minutes: 5, Entries: []float64{2400, -30},
			match: func(it item, ms mediaSource) bool {
				return (vid(ms, "av1", nil) || vid(ms, "vc1", nil) || vid(ms, "mpeg2video", nil)) && atLeast(it, 60)
			},
			Expect: expect(ex(tc, "", ""), ex("", "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-I25", Group: "Interaction", Title: "Transcode, seek backward: interlaced (deinterlace)",
			Dims: map[string]string{"video": "H.264 interlaced", "delivery": "Transcode SDR", "ops": "seek backward", "player": "SP-AV"},
			Risk: "Deinterlace doubles or keeps the field rate; after a restart the output frame rate and timestamps must match the first job.",
			Pass: passSeekTranscode + " No combing after the seek.", Minutes: 3, Entries: []float64{900, 300},
			match: func(it item, ms mediaSource) bool {
				return (vid(ms, "h264", func(v mediaStream) bool { return v.IsInterlaced }) || vid(ms, "mpeg2video", nil)) && atLeast(it, 20)
			},
			Expect: expect(ex(tc, "", ""), ex("", "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-I26", Group: "Interaction", Title: "Transcode, pause over 5 min then resume: AV1",
			Dims: map[string]string{"video": "AV1", "delivery": "Transcode SDR", "ops": "long pause", "player": "SP-AV"},
			Risk: "The reaped encoder job restarts at the paused segment; QSV session setup adds latency, and the client may time out the segment request.",
			Pass: passResumeTranscode + " Pause 6 min, then play.", Human: "Pause for over 5 min.", Minutes: 3, Entries: []float64{resumeAt},
			match:  func(_ item, ms mediaSource) bool { return vid(ms, "av1", nil) },
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-I27", Group: "Interaction", Title: "Two concurrent streams: a video transcode and a 4K audio-transcode remux",
			Dims: map[string]string{"delivery": "Transcode SDR + Remux a:transcode", "ops": "two concurrent streams", "player": "SP-AV"},
			Risk: "A QSV transcode and a CPU audio encode at once: if either runs below realtime, the client stalls; the two jobs must stay independent on seek.",
			Pass: "Play this item on the Apple TV and ATV-I15's item on another client; both play for 5 min without a stall; a seek on one leaves the other playing.", Human: "Needs a second client.", Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "av1", nil) || vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" })
			},
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},
		{
			ID: "ATV-I28", Group: "Interaction", Title: "Burn-in toggle on stock Swiftfin: PGS on and off mid-play",
			Dims: map[string]string{"subs": "PGS", "delivery": "Remux to Transcode (burn-in)", "ops": "subtitle on/off/switch", "player": "SF-AV"},
			Risk: "Turning PGS on changes the delivery from Remux to a burn-in Transcode at the current position, and off changes it back: two method changes and two off-grid restarts.",
			Pass: "Turn PGS on at about 3 min and off at about 6 min: each change resumes within 8 s at the same position, with subtitles in sync while on.", Human: "Stock Swiftfin build.", Minutes: 5, Entries: []float64{180, 360},
			match: func(_ item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return is1080(v) && rangeIs(v, "SDR") }) &&
					firstAudio(ms, isCopyAudio) && hasSub(ms, subCodec("PGSSUB", false, false))
			},
			subtitle: pickSub(subCodec("PGSSUB", false, false)),
			Expect:   expect(ex(rx, cp, "External"), ex(dp, cp, "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-I29", Group: "Interaction", Title: "Burn-in, resume on stock Swiftfin: ASS with fonts",
			Dims: map[string]string{"subs": "ASS with fonts", "delivery": "Transcode SDR (burn-in)", "ops": "resume off-grid", "player": "SF-AV"},
			Risk: "With -ss before the input, the subtitles filter sees timestamps from 0, so burned cues can shift by the resume offset unless the job keeps the source timestamps.",
			Pass: passResumeTranscode + " Burned-in cues match the dialogue after the resume.", Human: "Stock Swiftfin build; cue timing.", Minutes: 5, Entries: []float64{resumeAt},
			match: func(it item, ms mediaSource) bool {
				return hasFonts(ms) && hasSub(ms, subCodec("ass", false, false)) && vid(ms, "hevc", nil) && atLeast(it, 20)
			},
			subtitle: pickSub(subCodec("ass", false, false)),
			Expect:   expect(ex("", "", "External"), ex("", "", "Embed"), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-I30", Group: "Interaction", Title: "Transcode, next-episode autoplay: Hi10P anime with ASS",
			Dims: map[string]string{"video": "H.264 10-bit", "subs": "ASS with fonts", "delivery": "Transcode SDR", "ops": "next-episode autoplay", "player": "SP-AV"},
			Risk: "Encoder handover between episodes and font extraction for the next file; SharkParty must load the next episode's ASS track.",
			Pass: "Seek to the last minute; the next episode starts from 0 with sound and styled subtitles.", Human: "Autoplay and ASS on the next episode.", Minutes: 2, Entries: []float64{-60},
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" }) && hasSub(ms, subCodec("ass", false, false))
			},
			subtitle: pickSub(subCodec("ass", false, false)),
			Expect:   expect(ex(tc, "", "External"), ex(tc, "", ""), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-I31", Group: "Interaction", Title: "Transcode, dual audio switch: Hi10P anime Japanese to English",
			Dims: map[string]string{"video": "H.264 10-bit", "audio": "multiple tracks", "delivery": "Transcode SDR", "ops": "audio-track switch mid-play", "player": "SP-AV"},
			Risk: "A common anime shape: the audio switch restarts the full video transcode at the current position, with the restart latency of a seek.",
			Pass: passResumeTranscode + " Switch at about 5 min: the other language plays from the switch point.", Minutes: 5, Entries: []float64{300},
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High 10" }) && len(a) >= 2 && a[0].Language != a[1].Language
			},
			audio:  secondAudio,
			Expect: expect(ex(tc, "", ""), ex(tc, "", ""), ex(tc, "", "")),
		},

		// Video transcode from an HDR source: tone mapping.
		{
			ID: "ATV-I32", Group: "Interaction", Title: "Tone map, resume: Dolby Vision 5 on SharkParty VLC",
			Dims: map[string]string{"video": "Dolby Vision P5", "res": "4K", "delivery": "Transcode HDR to SDR", "ops": "resume off-grid", "player": "SP-VLC"},
			Risk: "VLC has no DOVI in its range list, so P5 transcodes with a tone map: a 4K decode, reshape and tone map must start within the client timeout and keep correct colors after the restart.",
			Pass: passResumeTranscode + " Colors are correct (no purple or green cast); the TV is in SDR.", Human: "Colors after the resume.", Minutes: 5, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVI") && is4K(v) })
			},
			Expect: expect(ex(rx, "", ""), ex(tc, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-I33", Group: "Interaction", Title: "Tone map, seek forward and back: 4K HDR10 + PGS burn-in on stock Swiftfin",
			Dims: map[string]string{"video": "HDR10", "res": "4K", "subs": "PGS", "delivery": "Transcode HDR (burn-in)", "ops": "seek forward far, seek backward", "player": "SF-AV"},
			Risk: "High-risk triple. Burn-in on a 4K HDR source is the heaviest job: 4K decode, overlay, tone map or HDR encode on the iGPU. It runs near realtime, so every seek risks a stall.",
			Pass: passSeekTranscode + " Colors are correct for the TV mode; PGS sits at the right size.", Human: "Stock Swiftfin build; colors and stalls.", Minutes: 5, Entries: []float64{2400, 600},
			match: func(it item, ms mediaSource) bool {
				return mkv4K(ms, "HDR10") && hasSub(ms, subCodec("PGSSUB", false, false)) && atLeast(it, 60)
			},
			subtitle: pickSub(subCodec("PGSSUB", false, false)),
			Expect:   expect(ex(rx, "", "External"), ex("", "", ""), ex(tc, "", "Encode")),
		},
		{
			ID: "ATV-I34", Group: "Interaction", Title: "Tone map, pause and background: Dolby Vision 5 on SharkParty VLC",
			Dims: map[string]string{"video": "Dolby Vision P5", "delivery": "Transcode HDR to SDR", "ops": "long pause, background/foreground", "player": "SP-VLC"},
			Risk: "The tone-map job is reaped during the pause; the restart must use the same tone-map settings, and the TV must stay in SDR.",
			Pass: passResumeTranscode + " Pause 6 min, play, then 2 min on the home screen: colors stay correct.", Human: "Pause and background.", Minutes: 3, Entries: []float64{resumeAt},
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVI") })
			},
			Expect: expect(ex(rx, "", ""), ex(tc, "", ""), ex(rx, "", "")),
		},
		{
			ID: "ATV-I35", Group: "Interaction", Title: "Two concurrent streams: a 4K tone map and a 4K remux",
			Dims: map[string]string{"res": "4K", "delivery": "Transcode HDR to SDR + Remux a:copy", "ops": "two concurrent streams", "player": "SP-VLC"},
			Risk: "The heaviest GPU job beside the heaviest I/O job: the tone map must stay above realtime.",
			Pass: "Play this item on SharkParty VLC and ATV-I01's item on another client for 5 min: no stall on either.", Human: "Needs a second client.", Minutes: 5,
			match: func(_ item, ms mediaSource) bool {
				return vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVI") && is4K(v) })
			},
			Expect: expect(ex(rx, "", ""), ex(tc, "", ""), ex(rx, "", "")),
		},

		// Direct play.
		{
			ID: "ATV-I36", Group: "Interaction", Title: "DirectPlay, resume and seek far on SharkParty VLC: 4K DV profile 7 + DTS-HD MA",
			Dims: map[string]string{"video": "Dolby Vision P7", "res": "4K", "audio": "DTS-HD MA", "delivery": "DirectPlay", "ops": "resume off-grid, seek forward far, seek backward", "player": "SP-VLC"},
			Risk: "A large UHD BD remux read by byte ranges: VLC seeks with the MKV Cues; a missing or late Cues element makes each seek read far into the file.",
			Pass: passSeek + " Resume, then seek +30 min and back -15 min.", Minutes: 3,
			match: func(it item, ms mediaSource) bool {
				return containerIs(ms, "mkv") && vid(ms, "hevc", func(v mediaStream) bool { return rangeIs(v, "DOVIWithEL") }) &&
					firstAudio(ms, codec("dts")) && atLeast(it, 90)
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
		{
			ID: "ATV-I37", Group: "Interaction", Title: "DirectPlay, seek near end and next episode: MP4 H.264 episode",
			Dims: map[string]string{"container": "MP4", "video": "H.264", "delivery": "DirectPlay", "ops": "seek near end, next-episode autoplay", "player": "SP-AV"},
			Risk: "A byte-range read at the end of the file (moov at the end needs an extra range); end of item reported by playstate, then the next PlaybackInfo.",
			Pass: "A seek to the last 30 s plays to the end; the next episode starts by itself.", Human: "Autoplay.", Minutes: 2,
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Episode" && isMP4(ms) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" || v.Profile == "Main" }) &&
					onlyAudio(ms, codec("aac")) && noSubs(ms)
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-I38", Group: "Interaction", Title: "DirectPlay, audio and subtitle switch on SharkParty VLC: DTS + a second track, PGS + SRT",
			Dims: map[string]string{"audio": "multiple tracks", "subs": "SRT + PGS", "delivery": "DirectPlay", "ops": "audio-track switch mid-play, subtitle on/off/switch", "player": "SP-VLC"},
			Risk: "VLC switches tracks inside the file; the server sees only playstate. The chosen indexes must reach the saved user data for the next resume.",
			Pass: passSubs + " Each switch applies within 2 s; stop and resume keeps the chosen tracks.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				a := streamsOf(ms, "Audio")
				return containerIs(ms, "mkv") && len(a) >= 2 && a[0].Codec == "dts" && a[1].Codec != "truehd" &&
					hasSub(ms, subCodec("PGSSUB", false, false)) && hasSub(ms, subCodec("subrip", false, false))
			},
			subtitle: pickSub(subCodec("subrip", false, false)),
			Expect:   expect(ex("", "", "External"), ex(dp, cp, "Embed"), ex("", "", "")),
		},
		{
			ID: "ATV-I39", Group: "Interaction", Title: "DirectPlay, pause over 5 min and background: MP4 H.264 movie",
			Dims: map[string]string{"container": "MP4", "delivery": "DirectPlay", "ops": "long pause, background/foreground", "player": "SP-AV"},
			Risk: "No server job, so this isolates the client: an idle HTTP connection closes and AVPlayer must reopen the range at the paused offset.",
			Pass: "Pause 6 min, play: picture and sound within 3 s; then 2 min on the home screen and return at the same position.", Human: "Pause and background.", Minutes: 2,
			match: func(it item, ms mediaSource) bool {
				return it.Type == "Movie" && isMP4(ms) && vid(ms, "h264", func(v mediaStream) bool { return v.Profile == "High" && is1080(v) }) && firstAudio(ms, codec("aac"))
			},
			Expect: expect(ex(dp, cp, ""), ex(dp, cp, ""), ex(dp, cp, "")),
		},
		{
			ID: "ATV-I40", Group: "Interaction", Title: "Two concurrent 4K direct plays on SharkParty VLC",
			Dims: map[string]string{"res": "4K", "delivery": "DirectPlay", "ops": "two concurrent streams", "player": "SP-VLC"},
			Risk: "Two high-bitrate byte-range readers on one disk and one uplink; a seek on one must not starve the other.",
			Pass: "Play this item on SharkParty VLC and a second 4K item on another client: no stall over 5 min.", Human: "Needs a second client.", Minutes: 3,
			match: func(_ item, ms mediaSource) bool {
				return mkv4K(ms, "DOVIWithHDR10") && firstAudio(ms, codec("dts"))
			},
			Expect: expect(ex(rx, at, ""), ex(dp, cp, ""), ex(rx, at, "")),
		},
	}
	for i := range out {
		if steps := operatorSteps(out[i]); steps != "" {
			out[i].Human = strings.TrimSpace(steps + " " + out[i].Human)
		}
	}
	return out
}

// operatorSteps returns the TV setup for a cell that names a player, as
// Siri Remote steps: the client settings, how to perform the operation,
// and the method that the player's Session panel must show, so that the
// operator sees when the wrong path is active. The control names are from
// SharkParty main (Swiftfin tvOS player overlay).
func operatorSteps(c cell) string {
	key, ok := map[string]string{"SP-AV": playerNative, "SP-VLC": playerVLC, "SF-AV": playerSwiftfin}[c.Dims["player"]]
	if !ok {
		return ""
	}
	const compat = "Settings > Video player > Playback quality > Device profile > Compatibility must be Auto."
	var parts []string
	switch key {
	case playerNative:
		parts = append(parts, "Setup: SharkParty, Settings > Video player: Native. "+compat)
	case playerVLC:
		parts = append(parts, "Setup: SharkParty, Settings > Video player: VLC. "+compat)
	case playerSwiftfin:
		parts = append(parts, "Setup: stock Swiftfin, Settings > Video player: Native. "+compat)
	}
	ops := c.Dims["ops"]
	direct := c.Expect[key].Method == dp
	var targets []string
	for _, e := range c.Entries {
		if e < 0 {
			targets = append(targets, fmt.Sprintf("the last %.0f s", -e))
		} else {
			targets = append(targets, clock(e))
		}
	}
	at := func(i int, fallback string) string {
		if i < len(targets) {
			return targets[i]
		}
		return fallback
	}
	has := func(op string) bool { return strings.Contains(ops, op) }
	if has("start at 0") {
		parts = append(parts, "Start: on the item page choose Play (or Play from beginning when it offers Resume).")
	}
	if has("resume") {
		if direct {
			parts = append(parts, "Resume is blocked on rc.8: the server saves no position for direct play. Until the fix lands, start from the beginning and scrub to the point instead.")
		} else {
			parts = append(parts, fmt.Sprintf("Make a resume point: play from the beginning and press Back at about %s. On a film, play past 6%% of the runtime instead (about 7:00 on a 110 min film): the server saves a position only after 5%% of the runtime. The item page must then show Resume; choose it. If it shows only Play, no position was saved.", at(0, "3:00")))
		}
	}
	scrub := "click the touch surface to show the controls, then hold right (or left) on the clickpad ring, or swipe on the touch surface, to scrub; release and click to play. One click right or left jumps 15 s."
	switch {
	case has("seek forward far") && has("seek backward"):
		parts = append(parts, fmt.Sprintf("Seek: play 1 min, then %s Scrub forward to %s, play 1 min, then scrub back to %s.", scrub, at(0, "+30 min"), at(1, "-15 min")))
	case has("seek forward far") && has("seek near end"):
		parts = append(parts, fmt.Sprintf("Seek: play 1 min, then %s Scrub to %s, play 1 min, then scrub to %s.", scrub, at(0, "+40 min"), at(1, "the last 30 s")))
	case has("seek forward far"):
		parts = append(parts, fmt.Sprintf("Seek: play 1 min, then %s Scrub to %s.", scrub, at(0, "+40 min")))
	case has("seek backward"):
		parts = append(parts, fmt.Sprintf("Seek: play 1 min, then %s Scrub forward to %s, play 1 min, then scrub back to %s.", scrub, at(0, "+15 min"), at(1, "5:00")))
	case has("seek near end"):
		parts = append(parts, fmt.Sprintf("Seek: play 1 min, then %s Scrub to %s.", scrub, at(0, "the last 30 s")))
	}
	if has("audio-track switch") {
		parts = append(parts, "Audio switch: at about 5:00, click the touch surface, swipe up to the action buttons, choose Audio and pick the other track.")
	}
	if has("subtitle") {
		parts = append(parts, "Subtitles: click the touch surface, swipe up to the action buttons, choose Subtitles; switch tracks and turn them off as the pass condition says.")
	}
	if has("long pause") {
		parts = append(parts, "Pause: press Play/Pause at about 3:00, wait 6 min (if the screen saver starts, press Play/Pause once to wake it), then press Play/Pause to play.")
	}
	if has("background") {
		parts = append(parts, "Background: press the TV button for the Home screen, wait 2 min, then open the app again from Home.")
	}
	if has("next-episode") {
		parts = append(parts, fmt.Sprintf("Next episode: scrub to %s and let the episode end without pressing anything; the next one must start by itself.", at(0, "the last minute")))
	}
	if has("two concurrent") {
		parts = append(parts, "Second stream: start this item on the Apple TV, then the second item on another client (phone app or web).")
	}
	if e := c.Expect[key]; e.Method != "" {
		parts = append(parts, fmt.Sprintf("Check the path: the expected delivery is %s. Until deadfin#635 ships, the Session panel Method reads Direct Play for every SharkParty session, so it does not prove the path: note the clock time you pressed Play, and the path is confirmed from the server ingress log. After deadfin#635, swipe down on the touch surface during play and open Session: Method must read %s.", expectText(e), sessionMethod(e))+
			" If it reads anything else, the wrong player, setting or track is active: stop, fix the setup, and do not record a result.")
	}
	return strings.Join(parts, " ")
}

// sessionMethod is the Method label of SharkParty's Session panel
// (SessionInfoDto.playMethodDisplayTitle) for a delivery.
func sessionMethod(e Expect) string {
	switch {
	case e.Method == dp:
		return "Direct Play"
	case e.Method == rx && e.Audio == at:
		return "Direct Stream"
	case e.Method == rx:
		return "Remux"
	case e.Method == tc:
		return "Transcode"
	}
	return e.Method
}

// clock formats seconds as m:ss.
func clock(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
