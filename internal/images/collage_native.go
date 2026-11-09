// SPDX-License-Identifier: GPL-2.0-only

//go:build cgo

package images

/*
#cgo pkg-config: vips
#include <vips/vips.h>
#include <stdlib.h>
#include <string.h>

enum { DEADFIN_COLLAGE_BAD_SOURCE = -2 };

static void deadfin_collage_unref(VipsImage *image) {
	if (image != NULL) g_object_unref(image);
}

// deadfin_collage_text renders text as a one-band mask. The text is escaped,
// as vips_text reads Pango markup.
static int deadfin_collage_text(VipsImage **out, const char *text, double size) {
	char font[64];
	gchar *escaped = g_markup_escape_text(text, -1);
	g_snprintf(font, sizeof(font), "sans %.3f", size);
	int result = vips_text(out, escaped, "font", font, "dpi", 72, NULL);
	g_free(escaped);
	return result;
}

// deadfin_thumb_collage ports StripCollageBuilder.BuildThumbCollageBitmap:
// a black canvas, the backdrop scaled to the canvas width at its own aspect,
// a black layer of alpha 0x78 over all of it, and the library name in white
// at size 112, scaled to 90% of the width when it is wider than 95%, centred.
// A path that does not decode returns DEADFIN_COLLAGE_BAD_SOURCE; a NULL
// path returns the black canvas alone.
static int deadfin_thumb_collage(const char *path, int width, int height, const char *name, void **output, size_t *output_length) {
	int result = -1;
	VipsImage *canvas = NULL, *in = NULL, *srgb = NULL, *flat = NULL, *three = NULL;
	VipsImage *scaled = NULL, *placed = NULL, *dark = NULL, *mask = NULL, *full_mask = NULL;
	VipsImage *labelled = NULL, *stage = NULL;

	if (vips_black(&canvas, width, height, "bands", 3, NULL)) goto done;
	stage = canvas;

	if (path != NULL) {
		in = vips_image_new_from_file(path, "access", VIPS_ACCESS_SEQUENTIAL, NULL);
		if (in == NULL) { result = DEADFIN_COLLAGE_BAD_SOURCE; goto done; }
		if (vips_colourspace(in, &srgb, VIPS_INTERPRETATION_sRGB, NULL)) { result = DEADFIN_COLLAGE_BAD_SOURCE; goto done; }
		VipsImage *opaque = srgb;
		if (vips_image_hasalpha(srgb)) {
			double black[1] = { 0.0 };
			if (vips_flatten(srgb, &flat, "background", vips_array_double_new(black, 1), NULL)) goto done;
			opaque = flat;
		}
		if (vips_image_get_bands(opaque) == 1) {
			VipsImage *bands[3] = { opaque, opaque, opaque };
			if (vips_bandjoin(bands, &three, 3, NULL)) goto done;
			opaque = three;
		}
		int source_width = vips_image_get_width(opaque), source_height = vips_image_get_height(opaque);
		if (source_width <= 0 || source_height <= 0) { result = DEADFIN_COLLAGE_BAD_SOURCE; goto done; }
		int backdrop_height = abs(width * source_height / source_width);
		if (backdrop_height < 1) backdrop_height = 1;
		if (vips_resize(opaque, &scaled, (double)width / source_width, "vscale", (double)backdrop_height / source_height, NULL)) { result = DEADFIN_COLLAGE_BAD_SOURCE; goto done; }
		if (vips_insert(canvas, scaled, &placed, 0, 0, NULL)) goto done;
		stage = placed;
	} else {
		// No image decodes: the black canvas, without the name.
		name = NULL;
		goto encode;
	}

	if (vips_linear1(stage, &dark, (255.0 - 0x78) / 255.0, 0.0, NULL)) goto done;
	stage = dark;

	if (name != NULL && name[0] != '\0') {
		double size = 112.0;
		if (deadfin_collage_text(&mask, name, size)) goto done;
		int text_width = vips_image_get_width(mask);
		if (text_width > width * 0.95) {
			size = 0.9 * width * size / text_width;
			deadfin_collage_unref(mask);
			mask = NULL;
			if (deadfin_collage_text(&mask, name, size)) goto done;
			text_width = vips_image_get_width(mask);
		}
		int left = (width - text_width) / 2;
		int top = (height - vips_image_get_height(mask)) / 2;
		if (vips_embed(mask, &full_mask, left, top, width, height, NULL)) goto done;
		double ink[3] = { 255.0, 255.0, 255.0 };
		double ones[3] = { 1.0, 1.0, 1.0 };
		VipsImage *ink_image = NULL, *ink_filled = NULL;
		int failed = vips_black(&ink_image, width, height, "bands", 3, NULL) ||
			vips_linear(ink_image, &ink_filled, ones, ink, 3, NULL) ||
			vips_ifthenelse(full_mask, ink_filled, stage, &labelled, "blend", TRUE, NULL);
		deadfin_collage_unref(ink_filled);
		deadfin_collage_unref(ink_image);
		if (failed) goto done;
		stage = labelled;
	}

encode:
	;
	VipsImage *encoded = NULL;
	if (vips_cast(stage, &encoded, VIPS_FORMAT_UCHAR, NULL)) goto done;
	VipsImage *tagged = NULL;
	if (vips_copy(encoded, &tagged, "interpretation", VIPS_INTERPRETATION_sRGB, NULL)) { deadfin_collage_unref(encoded); goto done; }
	result = vips_pngsave_buffer(tagged, output, output_length, NULL);
	deadfin_collage_unref(tagged);
	deadfin_collage_unref(encoded);

done:
	deadfin_collage_unref(labelled);
	deadfin_collage_unref(full_mask);
	deadfin_collage_unref(mask);
	deadfin_collage_unref(dark);
	deadfin_collage_unref(placed);
	deadfin_collage_unref(scaled);
	deadfin_collage_unref(three);
	deadfin_collage_unref(flat);
	deadfin_collage_unref(srgb);
	deadfin_collage_unref(in);
	deadfin_collage_unref(canvas);
	if (result != 0) vips_error_clear();
	return result;
}

static void deadfin_collage_free(void *value) {
	g_free(value);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// CollageBuilder creates the library collages of the dynamic image
// providers (SkiaEncoder.CreateImageCollage).
type CollageBuilder struct {
	runtime *Runtime
}

// NewCollageBuilder returns a collage builder that runs on runtime.
func NewCollageBuilder(runtime *Runtime) *CollageBuilder {
	return &CollageBuilder{runtime: runtime}
}

// ThumbCollage ports StripCollageBuilder.BuildThumbCollage. It draws the
// first path that decodes (SkiaHelper.GetNextValidImage) as the backdrop and
// writes libraryName over it. It returns a PNG image.
func (builder *CollageBuilder) ThumbCollage(ctx context.Context, paths []string, width, height int, libraryName string) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("collage size %dx%d", width, height)
	}
	if err := builder.runtime.begin(); err != nil {
		return nil, err
	}
	defer builder.runtime.end()
	name := C.CString("")
	if strings.TrimSpace(libraryName) != "" {
		C.free(unsafe.Pointer(name))
		name = C.CString(libraryName)
	}
	defer C.free(unsafe.Pointer(name))
	for index := 0; index <= len(paths); index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var path *C.char
		if index < len(paths) {
			path = C.CString(paths[index])
		}
		var output unsafe.Pointer
		var length C.size_t
		result := C.deadfin_thumb_collage(path, C.int(width), C.int(height), name, &output, &length)
		if path != nil {
			C.free(unsafe.Pointer(path))
		}
		if result == C.DEADFIN_COLLAGE_BAD_SOURCE {
			continue
		}
		if result != 0 {
			return nil, errors.New("create thumb collage")
		}
		data := C.GoBytes(output, C.int(length))
		C.deadfin_collage_free(output)
		return data, nil
	}
	return nil, errors.New("create thumb collage: no canvas")
}
