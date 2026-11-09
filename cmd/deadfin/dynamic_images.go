// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"runtime"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/config"
	"github.com/deadfin-org/deadfin/internal/encoding"
	"github.com/deadfin-org/deadfin/internal/ffmpeg"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/providers"
)

// newDynamicImages composes the Screen Grabber and the Embedded Image
// Extractor. The extraction pool size is ParallelImageEncodingLimit, or the
// processor count, read once at startup as MediaEncoder does. A nil collage
// builder creates no library images.
func newDynamicImages(manager *config.Manager, capabilities func() encoding.Capabilities, metadataPath string, collage *images.CollageBuilder) *providers.DynamicImages {
	parallel := manager.System().ParallelImageEncodingLimit
	if parallel < 1 {
		parallel = runtime.NumCPU()
	}
	var collager providers.ThumbCollager
	if collage != nil {
		collager = collage
	}
	return providers.NewDynamicImages(providers.DynamicImagesOptions{
		Collage:      collager,
		Extractor:    ffmpeg.NewImageExtractor("ffmpeg", parallel),
		MetadataPath: metadataPath,
		Settings: func(itemType string) providers.DynamicImageSettings {
			return dynamicImageSettings(manager.System(), manager.Encoding(), capabilities(), itemType)
		},
	})
}

// dynamicImageSettings projects the server configuration and encoder facts
// that MediaEncoder.ExtractImageInternal and ProviderManager read.
func dynamicImageSettings(system config.ServerConfiguration, options config.EncodingOptions, capabilities encoding.Capabilities, itemType string) providers.DynamicImageSettings {
	settings := providers.DynamicImageSettings{
		Extraction: encoding.ImageExtraction{
			Resolution:       imageResolutionParameter(system.ChapterImageResolution),
			Threads:          imageExtractionThreads(options.EncodingThreadCount),
			SupportsTonemapX: capabilities.SupportsTonemapX,
			SupportsZscale:   containsCapability(capabilities.Filters, "zscale"),
			EncoderAtLeast51: capabilities.EncoderMajorVersion > 5 || capabilities.EncoderMajorVersion == 5 && capabilities.EncoderMinorVersion >= 1,
		},
	}
	if system.ImageExtractionTimeoutMs > 0 {
		settings.Timeout = time.Duration(system.ImageExtractionTimeoutMs) * time.Millisecond
	}
	for _, metadata := range system.MetadataOptions {
		if strings.EqualFold(metadata.ItemType, itemType) {
			settings.DisabledImageFetchers = metadata.DisabledImageFetchers
			settings.ImageFetcherOrder = metadata.ImageFetcherOrder
			break
		}
	}
	return settings
}

// imageResolutionParameter ports MediaEncoder.GetImageResolutionParameter.
func imageResolutionParameter(resolution config.ImageResolution) string {
	switch resolution {
	case config.ImageResolutionP144:
		return "256x144"
	case config.ImageResolutionP240:
		return "426x240"
	case config.ImageResolutionP360:
		return "640x360"
	case config.ImageResolutionP480:
		return "854x480"
	case config.ImageResolutionP720:
		return "1280x720"
	case config.ImageResolutionP1080:
		return "1920x1080"
	case config.ImageResolutionP1440:
		return "2560x1440"
	case config.ImageResolutionP2160:
		return "3840x2160"
	default:
		return ""
	}
}

// imageExtractionThreads ports EncodingHelper.GetNumberOfThreads without a
// job: zero lets ffmpeg decide.
func imageExtractionThreads(count int) int {
	if count <= 0 {
		return 0
	}
	return min(count, runtime.NumCPU())
}
