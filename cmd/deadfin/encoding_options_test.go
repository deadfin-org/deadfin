// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
)

func TestHardwareOptionsProjectsEncodingXMLAndClearsMissingRenderNodes(t *testing.T) {
	options := config.DefaultEncodingOptions()
	options.HardwareAccelerationType = "qsv"
	options.EnableTonemapping = true
	options.EnableVppTonemapping = true
	options.QsvDevice = "/dev/dri/renderD129"
	options.HardwareDecodingCodecs = []string{"h264", "hevc"}
	exists := func(path string) bool { return path == "/dev/dri/renderD128" }

	got := hardwareOptions(options, exists)
	want := encoding.HardwareOptions{
		Type:                           "qsv",
		EnableHardwareEncoding:         true,
		HardwareDecodingCodecs:         []string{"h264", "hevc"},
		EnableDecodingColorDepth10Hevc: true,
		EnableDecodingColorDepth10Vp9:  true,
		PreferSystemNativeHwDecoder:    true,
		EnableEnhancedNvdecDecoder:     true,
		EnableTonemapping:              true,
		EnableVppTonemapping:           true,
		Tonemapping:                    encoding.DefaultTonemapping(),
		VppTonemappingBrightness:       16,
		VppTonemappingContrast:         1,
		VaapiDevice:                    "/dev/dri/renderD128",
		EncoderPreset:                  "auto",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hardwareOptions =\n%#v\nwant\n%#v", got, want)
	}
}

func TestEncodingCapabilitiesProjectsHardwareProbe(t *testing.T) {
	got := encodingCapabilities(ffmpeg.Capabilities{
		Version:    ffmpeg.Version{Major: 8, Minor: 1, Patch: 1},
		Encoders:   []string{"h264_qsv", "libx264"},
		Decoders:   []string{"hevc_qsv"},
		Filters:    []string{"tonemap_vaapi", "tonemapx"},
		HWAccels:   []string{"qsv", "vaapi"},
		BSFOptions: []string{"dovi_rpu=strip", "hevc_metadata=remove_dovi"},
	}, ffmpeg.NvidiaCapabilities{})
	if !got.SupportsTonemapX || !got.SupportsBSFOption("hevc_metadata=remove_dovi") || got.H264Encoder != "libx264" || got.EncoderMinorVersion != 1 || got.EncoderPatchVersion != 1 ||
		!reflect.DeepEqual(got.HWAccels, []string{"qsv", "vaapi"}) || !reflect.DeepEqual(got.Encoders, []string{"h264_qsv", "libx264"}) {
		t.Fatalf("encodingCapabilities = %#v", got)
	}
}

func TestEncodingCapabilitiesKeepsProbedNvidiaFacts(t *testing.T) {
	probed := ffmpeg.Capabilities{
		Encoders: []string{"av1_nvenc", "h264_nvenc", "h264_qsv", "hevc_nvenc", "libx264"},
		HWAccels: []string{"cuda", "qsv", "vaapi"},
	}
	ampere := encodingCapabilities(probed, ffmpeg.NvidiaCapabilities{Device: true, Encoders: []string{"h264_nvenc", "hevc_nvenc"}, OverlayCudaAlphaFormat: true})
	if !reflect.DeepEqual(ampere.Encoders, []string{"h264_nvenc", "h264_qsv", "hevc_nvenc", "libx264"}) ||
		!reflect.DeepEqual(ampere.HWAccels, []string{"cuda", "qsv", "vaapi"}) || !ampere.SupportsOverlayCudaAlphaFormat {
		t.Fatalf("encodingCapabilities with an Ampere device = %#v", ampere)
	}
	noDevice := encodingCapabilities(probed, ffmpeg.NvidiaCapabilities{})
	if !reflect.DeepEqual(noDevice.Encoders, []string{"h264_qsv", "libx264"}) || !reflect.DeepEqual(noDevice.HWAccels, []string{"qsv", "vaapi"}) {
		t.Fatalf("encodingCapabilities without a device = %#v", noDevice)
	}
}

func TestProbeVaapiDriverSkipsUnlessVaapiIsConfigured(t *testing.T) {
	caps := encoding.Capabilities{HWAccels: []string{"vaapi"}}
	options := config.DefaultEncodingOptions()
	options.HardwareAccelerationType = "qsv"
	if got := probeVaapiDriver(t.Context(), caps, options); got.VaapiDriver != "" {
		t.Fatalf("VaapiDriver = %q, want no probe for QSV", got.VaapiDriver)
	}
}
