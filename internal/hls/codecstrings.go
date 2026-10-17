// SPDX-License-Identifier: GPL-2.0-only

package hls

import (
	"strconv"
	"strings"
)

// VideoCodec describes the source facts required to render an RFC 6381 video
// codec token. Dolby Vision takes precedence when it is present because it is
// the client-selectable codec for profiles without a compatible base layer.
type VideoCodec struct {
	Codec              string
	Profile            string
	Level              int
	HighTier           bool
	BitDepth           int
	Width              int
	Height             int
	PixelFormat        string
	FrameRate          float64
	DolbyVisionProfile int
	DolbyVisionLevel   int
}

// AudioCodec describes the source facts required to render an RFC 6381 audio
// codec token.
type AudioCodec struct {
	Codec   string
	Profile string
}

// VideoCodecString formats codec as the RFC 6381 CODECS token used by HLS.
func VideoCodecString(codec VideoCodec) string {
	if codec.DolbyVisionProfile != 0 && codec.DolbyVisionLevel != 0 {
		return doviCodecString(codec.DolbyVisionProfile, codec.DolbyVisionLevel, codec.Codec)
	}

	switch {
	case strings.EqualFold(codec.Codec, "h264"):
		return h264CodecString(codec.Profile, codec.Level)
	case strings.EqualFold(codec.Codec, "h265"), strings.EqualFold(codec.Codec, "hevc"):
		return h265CodecString(codec.Profile, codec.Level, codec.HighTier)
	case strings.EqualFold(codec.Codec, "av1"):
		return av1CodecString(codec.Profile, codec.Level, codec.HighTier, codec.BitDepth)
	case strings.EqualFold(codec.Codec, "vp9"):
		return vp9CodecString(codec.Width, codec.Height, codec.PixelFormat, codec.FrameRate, codec.BitDepth)
	default:
		return ""
	}
}

// AudioCodecString formats codec as the RFC 6381 CODECS token used by HLS.
func AudioCodecString(codec AudioCodec) string {
	switch {
	case strings.EqualFold(codec.Codec, "aac"):
		if strings.EqualFold(codec.Profile, "HE-AAC") {
			return "mp4a.40.5"
		}
		return "mp4a.40.2"
	case strings.EqualFold(codec.Codec, "mp3"):
		return "mp4a.40.34"
	case strings.EqualFold(codec.Codec, "ac3"):
		return "ac-3"
	case strings.EqualFold(codec.Codec, "eac3"):
		return "ec-3"
	case strings.EqualFold(codec.Codec, "flac"):
		return "fLaC"
	case strings.EqualFold(codec.Codec, "alac"):
		return "alac"
	case strings.EqualFold(codec.Codec, "opus"):
		return "Opus"
	case strings.EqualFold(codec.Codec, "truehd"):
		return "mlpa"
	case strings.EqualFold(codec.Codec, "dts"):
		return dtsCodecString(codec.Profile)
	default:
		return ""
	}
}

func h264CodecString(profile string, level int) string {
	profileHex := "4240"
	switch {
	case strings.EqualFold(profile, "high"):
		profileHex = "6400"
	case strings.EqualFold(profile, "main"):
		profileHex = "4D40"
	case strings.EqualFold(profile, "baseline"):
		profileHex = "42E0"
	}
	levelHex := strings.ToUpper(strconv.FormatInt(int64(level), 16))
	if len(levelHex) == 1 {
		levelHex = "0" + levelHex
	}
	return "avc1." + profileHex + levelHex
}

func h265CodecString(profile string, level int, highTier bool) string {
	profilePart := "1.4"
	if strings.EqualFold(profile, "main10") || strings.EqualFold(profile, "main 10") {
		profilePart = "2.4"
	}
	tier := "L"
	if highTier {
		tier = "H"
	}
	return "hvc1." + profilePart + "." + tier + strconv.Itoa(level) + ".B0"
}

func av1CodecString(profile string, level int, highTier bool, bitDepth int) string {
	profileNumber := "0"
	switch {
	case strings.EqualFold(profile, "high"):
		profileNumber = "1"
	case strings.EqualFold(profile, "professional"):
		profileNumber = "2"
	}
	if level <= 0 || level > 31 {
		level = 19
	}
	if bitDepth != 8 && bitDepth != 10 && bitDepth != 12 {
		bitDepth = 8
	}
	tier := byte('M')
	if highTier {
		tier = 'H'
	}
	return "av01." + profileNumber + "." + twoDigits(level) + string(tier) + "." + twoDigits(bitDepth)
}

func doviCodecString(profile, level int, codec string) string {
	fourCC := "dvh1"
	if strings.EqualFold(codec, "av1") {
		fourCC = "dav1"
	}
	return fourCC + "." + twoDigits(profile) + "." + twoDigits(level)
}

func vp9CodecString(width, height int, pixelFormat string, frameRate float64, bitDepth int) string {
	profile := "00"
	switch pixelFormat {
	case "yuv422p", "yuv444p":
		profile = "01"
	case "yuv420p10le", "yuv420p12le":
		profile = "02"
	case "yuv422p10le", "yuv422p12le", "yuv444p10le", "yuv444p12le":
		profile = "03"
	}
	pixels := width * height
	sampleRate := float64(pixels) * frameRate
	level := "00"
	switch {
	case pixels <= 0:
	case pixels <= 36864:
		level = "10"
	case pixels <= 73728:
		level = "11"
	case pixels <= 122880:
		level = "20"
	case pixels <= 245760:
		level = "21"
	case pixels <= 552960:
		level = "30"
	case pixels <= 983040:
		level = "31"
	case pixels <= 2228224:
		if sampleRate <= 83558400 {
			level = "40"
		} else {
			level = "41"
		}
	case pixels <= 8912896:
		if sampleRate <= 311951360 {
			level = "50"
		} else if sampleRate <= 588251136 {
			level = "51"
		} else {
			level = "52"
		}
	case pixels <= 35651584:
		if sampleRate <= 1176502272 {
			level = "60"
		} else if sampleRate <= 4706009088 {
			level = "61"
		} else {
			level = "62"
		}
	}
	if bitDepth != 8 && bitDepth != 10 && bitDepth != 12 {
		bitDepth = 8
	}
	return "vp09." + profile + "." + level + "." + twoDigits(bitDepth)
}

func dtsCodecString(profile string) string {
	switch {
	case strings.EqualFold(profile, "DTS Express"):
		return "dtse"
	case strings.EqualFold(profile, "DTS-HD HRA"), strings.EqualFold(profile, "DTS-HD MA"), strings.EqualFold(profile, "DTS-HD MA + DTS:X"), strings.EqualFold(profile, "DTS-HD MA + DTS:X IMAX"):
		return "dtsh"
	default:
		return "dtsc"
	}
}

func twoDigits(value int) string {
	if value >= 0 && value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}
