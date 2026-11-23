// SPDX-License-Identifier: GPL-2.0-only

package encoding

import (
	"strings"
	"testing"
)

// The goldens in this file are the argv the pinned EncodingHelper.cs builds
// for HardwareAccelerationType nvenc on Linux with jellyfin-ffmpeg 8.1:
// GetInputVideoHwaccelArgs (:1141-1157), GetNvdecVidDecoder (:6823-6895),
// GetHwaccelType (:6685-6700), GetNvidiaVidFiltersPrefered (:4001-4174),
// GetHwTonemapFilter (:3691-3772), GetEncoderParam (:1809-1823),
// GetVideoBitrateParam (:1626-1723), and GetHlsVideoKeyFrameArguments
// (:2004-2087). No GPU is needed: the builder is pure.

// nvidiaCapabilities is jellyfin-ffmpeg 8.1 on an Ada GPU (RTX 4090) after
// the composition root keeps the NVENC encoders that the device accepts.
func nvidiaCapabilities() Capabilities {
	caps := jellyfinFFmpegCapabilities()
	caps.Encoders = []string{"av1_nvenc", "h264_nvenc", "hevc_nvenc", "libx264"}
	caps.Decoders = []string{"av1_cuvid", "h264_cuvid", "hevc_cuvid", "mpeg2_cuvid", "vc1_cuvid", "vp9_cuvid"}
	caps.HWAccels = []string{"cuda", "drm", "opencl", "vaapi"}
	caps.Filters = []string{
		"alphasrc", "hwupload_cuda", "overlay_cuda", "scale_cuda", "subtitles", "tonemap_cuda", "tonemapx", "yadif_cuda",
	}
	caps.SupportsOverlayCudaAlphaFormat = true
	return caps
}

// ampereCapabilities is the same build on an Ampere GPU (RTX A2000): the
// device rejects av1_nvenc, so the composition root drops it.
func ampereCapabilities() Capabilities {
	caps := nvidiaCapabilities()
	caps.Encoders = []string{"h264_nvenc", "hevc_nvenc", "libx264"}
	return caps
}

func nvencOptions() HardwareOptions {
	return HardwareOptions{
		Type:                           HardwareAccelerationNVENC,
		EnableHardwareEncoding:         true,
		HardwareDecodingCodecs:         []string{"h264", "hevc", "vp9", "av1"},
		EnableDecodingColorDepth10Hevc: true,
		EnableDecodingColorDepth10Vp9:  true,
		EnableEnhancedNvdecDecoder:     true,
		EnableTonemapping:              true,
		Tonemapping:                    DefaultTonemapping(),
		EncoderPreset:                  "auto",
	}
}

const (
	cudaDevice  = "-init_hw_device cuda=cu:0 -filter_hw_device cu"
	nvdecInput  = "-hwaccel cuda -hwaccel_output_format cuda -noautorotate -hwaccel_flags +unsafe_output -threads 1"
	cudaTonemap = "tonemap_cuda=format=yuv420p:p=bt709:t=bt709:m=bt709:tonemap=bt2390:peak=100:desat=0"
	fmp4Tail    = "-codec:a:0 copy -copyts -avoid_negative_ts disabled -max_muxing_queue_size 2048 -f hls -max_delay 5000000 -hls_time 3 -hls_segment_type fmp4 -hls_fmp4_init_filename abc-1.mp4 -start_number 0 -hls_segment_filename /cache/transcodes/abc%d.mp4 -hls_playlist_type vod -hls_list_size 0 -hls_segment_options movflags=+frag_discont+skip_sidx -y /cache/transcodes/abc.m3u8"
)

func TestBuildHLSArgsNvidiaGoldens(t *testing.T) {
	withoutCuda := nvidiaCapabilities()
	withoutCuda.HWAccels = []string{"drm", "opencl", "vaapi"}
	withoutDevice := nvidiaCapabilities()
	withoutDevice.Encoders = []string{"libx264"}
	withoutDevice.HWAccels = []string{"drm", "opencl", "vaapi"}
	withoutAlphaFormat := nvidiaCapabilities()
	withoutAlphaFormat.SupportsOverlayCudaAlphaFormat = false

	for _, test := range []struct {
		name string
		job  HLSJob
		caps Capabilities
		want []string
	}{
		{
			// The device probe failed, so no NVENC encoder is listed:
			// software decode, tonemapx, and libx264.
			name: "software HDR10 when the GPU is unavailable",
			job:  withHardware(hevcHDR10Job(), nvencOptions()),
			caps: withoutDevice,
			want: argv("-i file:/media/movies/1917.mkv", hlsMapCopyAudio, x264Encode8M,
				"-vf", hdrSetparams+",scale=trunc(1920/2)*2:trunc(1080/2)*2,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p",
				hlsOutputTail),
		},
		{
			// IsCudaFullSupported fails: the legacy software chain feeds
			// system-memory frames to NVENC, as upstream does.
			name: "software filters into NVENC when CUDA filters are unavailable",
			job:  withHardware(hevcHDR10Job(), nvencOptions()),
			caps: withoutCuda,
			want: argv("-i file:/media/movies/1917.mkv", hlsMapCopyAudio,
				"-codec:v:0 h264_nvenc -preset p1 -b:v 8000000 -maxrate 8000000 -bufsize 16000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-vf", hdrSetparams+",scale=trunc(1920/2)*2:trunc(1080/2)*2,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p",
				hlsOutputTail),
		},
		{
			// NVDEC into VRAM, scale_cuda to yuv420p, h264_nvenc.
			name: "NVENC SDR H.264 full hardware",
			job:  withHardware(h264SDRJob(), nvencOptions()),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/aliens.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 h264_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-vf", sdrSetparams+",scale_cuda=w=1280:h=720:format=yuv420p",
				hlsOutputTail),
		},
		{
			// tonemap_cuda maps HDR10 to SDR in VRAM before h264_nvenc.
			name: "NVENC H.264 from HEVC 10-bit HDR10 with tonemap_cuda",
			job:  withHardware(hevcHDR10Job(), nvencOptions()),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 h264_nvenc -preset p1 -b:v 8000000 -maxrate 8000000 -bufsize 16000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-vf", hdrSetparams+",scale_cuda=w=1920:h=1080,"+cudaTonemap,
				hlsOutputTail),
		},
		{
			// HEVC Main 10 HDR10 source to an HEVC Main SDR fMP4 output.
			name: "NVENC HEVC from HEVC 10-bit HDR10 with tonemap_cuda",
			job: func() HLSJob {
				job := withHardware(hevcHDR10Job(), nvencOptions())
				job.OutputContainer = "fmp4"
				job.OutputVideoCodec = "hevc"
				job.OutputVideoProfile = "main10"
				job.Hardware.EncoderPreset = "medium"
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 hevc_nvenc -tag:v:0 hvc1 -preset p4 -b:v 8000000 -maxrate 8000000 -bufsize 16000000 -profile:v:0 main -g:v:0 72 -keyint_min:v:0 72",
				"-vf", hdrSetparams+",scale_cuda=w=1920:h=1080,"+cudaTonemap,
				fmp4Tail),
		},
		{
			// Graphical subtitles upload to CUDA and overlay in VRAM.
			name: "NVENC HDR10 with PGS burn-in via overlay_cuda",
			job: func() HLSJob {
				job := withHardware(hevcHDR10Job(), nvencOptions())
				job.SubtitleStream = &SubtitleStream{Index: 3, Codec: "hdmv_pgs_subtitle", BurnIn: true}
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/1917.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0",
				"-codec:v:0 h264_nvenc -preset p1 -b:v 8000000 -maxrate 8000000 -bufsize 16000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-filter_complex", "[0:3]scale,scale=-1:1080:fast_bilinear,crop,pad=max(1920\\,iw):max(1080\\,ih):(ow-iw)/2:(oh-ih)/2:black@0,crop=1920:1080,format=yuva420p,hwupload=derive_device=cuda[sub];"+
					"[0:0]"+hdrSetparams+",scale_cuda=w=1920:h=1080,"+cudaTonemap+"[main];"+
					"[main][sub]overlay_cuda=eof_action=pass:repeatlast=0",
				"-start_at_zero", hlsOutputTail),
		},
		{
			// Text subtitles render on a yuva420p alphasrc canvas.
			name: "NVENC SDR with text subtitle burn-in",
			job: func() HLSJob {
				job := withHardware(h264SDRJob(), nvencOptions())
				// File stream 2 is the first subtitle stream: si= counts subtitle streams only.
				job.SubtitleStream = &SubtitleStream{Index: 2, SubtitleIndex: 0, Codec: "subrip", BurnIn: true}
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/aliens.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0",
				"-codec:v:0 h264_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-filter_complex", "alphasrc=s=1280x720:r=10:start='0',format=yuva420p,subtitles=f='/media/movies/aliens.mkv':si=0:alpha=1:sub2video=1,hwupload=derive_device=cuda[sub];"+
					"[0:0]"+sdrSetparams+",scale_cuda=w=1280:h=720:format=yuv420p[main];"+
					"[main][sub]overlay_cuda=eof_action=pass:repeatlast=0:alpha_format=premultiplied",
				"-start_at_zero", hlsOutputTail),
		},
		{
			name: "NVENC text burn-in without the alpha_format option",
			job: func() HLSJob {
				job := withHardware(h264SDRJob(), nvencOptions())
				// File stream 2 is the first subtitle stream: si= counts subtitle streams only.
				job.SubtitleStream = &SubtitleStream{Index: 2, SubtitleIndex: 0, Codec: "subrip", BurnIn: true}
				return job
			}(),
			caps: withoutAlphaFormat,
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/aliens.mkv -noautoscale",
				"-map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -map -0:0",
				"-codec:v:0 h264_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-filter_complex", "alphasrc=s=1280x720:r=10:start='0',format=yuva420p,subtitles=f='/media/movies/aliens.mkv':si=0:alpha=1:sub2video=1,hwupload=derive_device=cuda[sub];"+
					"[0:0]"+sdrSetparams+",scale_cuda=w=1280:h=720:format=yuv420p[main];"+
					"[main][sub]overlay_cuda=eof_action=pass:repeatlast=0",
				"-start_at_zero", hlsOutputTail),
		},
		{
			// AV1 output where the device accepts av1_nvenc.
			name: "NVENC AV1 output on Ada",
			job: func() HLSJob {
				job := withHardware(h264SDRJob(), nvencOptions())
				job.OutputContainer = "fmp4"
				job.OutputVideoCodec = "av1"
				job.OutputVideoProfile = "main"
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/aliens.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 av1_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -g:v:0 72 -keyint_min:v:0 72",
				"-vf", sdrSetparams+",scale_cuda=w=1280:h=720:format=yuv420p",
				fmp4Tail),
		},
		{
			// H.264 High 10 has no NVDEC decoder: software decode, NVENC
			// encode from system memory.
			name: "NVENC from a software-decoded source",
			job: func() HLSJob {
				job := withHardware(h264SDRJob(), nvencOptions())
				job.VideoStream.BitDepth = 10
				job.VideoStream.PixelFormat = "yuv420p10le"
				job.VideoStream.Profile = "High 10"
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, "-i file:/media/movies/aliens.mkv", hlsMapCopyAudio,
				"-codec:v:0 h264_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-vf", sdrSetparams+",scale=trunc(1280/2)*2:trunc(720/2)*2,format=yuv420p",
				hlsOutputTail),
		},
		{
			// EnableEnhancedNvdecDecoder off selects the cuvid decoder.
			name: "NVENC with the cuvid decoder",
			job: func() HLSJob {
				job := withHardware(h264SDRJob(), nvencOptions())
				job.Hardware.EnableEnhancedNvdecDecoder = false
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, "-hwaccel cuda -hwaccel_output_format cuda -noautorotate -c:v h264_cuvid -i file:/media/movies/aliens.mkv -noautoscale", hlsMapCopyAudio,
				"-codec:v:0 h264_nvenc -preset p1 -b:v 4000000 -maxrate 4000000 -bufsize 8000000 -profile:v:0 high -g:v:0 72 -keyint_min:v:0 72",
				"-vf", sdrSetparams+",scale_cuda=w=1280:h=720:format=yuv420p",
				hlsOutputTail),
		},
		{
			// NVDEC with a software encoder downloads the frames.
			name: "NVDEC decode with software encode",
			job: func() HLSJob {
				job := withHardware(hevcHDR10Job(), nvencOptions())
				job.Hardware.EnableHardwareEncoding = false
				return job
			}(),
			caps: nvidiaCapabilities(),
			want: argv(cudaDevice, nvdecInput, "-i file:/media/movies/1917.mkv -noautoscale", hlsMapCopyAudio, x264Encode8M,
				"-vf", hdrSetparams+",scale_cuda=w=1920:h=1080,"+cudaTonemap+",hwdownload,format=yuv420p",
				hlsOutputTail),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildHLSArgs(test.job, test.caps)
			if err != nil {
				t.Fatalf("BuildHLSArgs: %v", err)
			}
			if difference := argvDifference(test.want, got); difference != "" {
				t.Fatalf("argv mismatch\n got: %s\n%s", strings.Join(got, " "), difference)
			}
		})
	}
}

func withHardware(job HLSJob, options HardwareOptions) HLSJob {
	job.Hardware = options
	return job
}

// An Ampere card has no AV1 encoder. Selection follows the probed encoder
// list, not the card name: AV1 is unavailable and an AV1 job is rejected
// instead of starting an ffmpeg that fails.
func TestNvencAV1SelectedOnlyWhenAvailable(t *testing.T) {
	options := nvencOptions()
	for _, test := range []struct {
		name  string
		caps  Capabilities
		codec string
		want  bool
	}{
		{"Ada AV1", nvidiaCapabilities(), "av1", true},
		{"Ada HEVC", nvidiaCapabilities(), "hevc", true},
		{"Ampere AV1", ampereCapabilities(), "av1", false},
		{"Ampere HEVC", ampereCapabilities(), "h265", true},
		{"Ampere H.264", ampereCapabilities(), "h264", true},
		{"software HEVC", jellyfinFFmpegCapabilities(), "hevc", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CanEncodeVideo(test.codec, test.caps, options); got != test.want {
				t.Fatalf("CanEncodeVideo(%q) = %v, want %v", test.codec, got, test.want)
			}
		})
	}
	if got := CanEncodeVideo("av1", nvidiaCapabilities(), HardwareOptions{}); got {
		t.Fatal("CanEncodeVideo(av1) without hardware encoding = true")
	}

	job := withHardware(h264SDRJob(), options)
	job.OutputContainer = "fmp4"
	job.OutputVideoCodec = "av1"
	if _, err := BuildHLSArgs(job, ampereCapabilities()); err == nil {
		t.Fatal("BuildHLSArgs accepted AV1 output without an AV1 encoder")
	}
	got, err := BuildHLSArgs(job, nvidiaCapabilities())
	if err != nil {
		t.Fatalf("BuildHLSArgs on Ada: %v", err)
	}
	if !strings.Contains(strings.Join(got, " "), "-codec:v:0 av1_nvenc") {
		t.Fatalf("argv %q does not select av1_nvenc", got)
	}
}

func TestNvencPresetMapping(t *testing.T) {
	for preset, want := range map[string]string{
		"veryslow": "p7", "slower": "p6", "slow": "p5", "medium": "p4", "fast": "p3", "faster": "p2",
		"veryfast": "p1", "superfast": "p1", "ultrafast": "p1", "auto": "p1", "": "p1",
	} {
		if got := nvencPreset(preset); got != want {
			t.Errorf("nvencPreset(%q) = %q, want %q", preset, got, want)
		}
	}
}

func TestCanTonemapNvidia(t *testing.T) {
	hdr10 := hevcHDR10Job().VideoStream
	dv5 := hdr10
	dv5.RangeType = "DOVI"
	dv5.DolbyVisionProfile = 5
	noTonemapX := nvidiaCapabilities()
	noTonemapX.SupportsTonemapX = false
	noCuda := noTonemapX
	noCuda.HWAccels = []string{"drm", "vaapi"}
	disabled := nvencOptions()
	disabled.EnableTonemapping = false

	for _, test := range []struct {
		name    string
		stream  VideoStream
		caps    Capabilities
		options HardwareOptions
		want    bool
	}{
		{"tonemap_cuda", hdr10, noTonemapX, nvencOptions(), true},
		{"tonemap_cuda maps DV profile 5", dv5, noTonemapX, nvencOptions(), true},
		{"tone mapping disabled", hdr10, noTonemapX, disabled, false},
		{"no CUDA", hdr10, noCuda, nvencOptions(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CanTonemap(test.stream, test.caps, test.options); got != test.want {
				t.Fatalf("CanTonemap = %v, want %v", got, test.want)
			}
		})
	}
}
