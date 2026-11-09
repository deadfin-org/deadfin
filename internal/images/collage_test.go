// SPDX-License-Identifier: GPL-2.0-only

package images_test

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/images"
)

const collageSubprocessMode = "DEADFIN_COLLAGE_SUBPROCESS"

// TestThumbCollage checks the library collage of
// StripCollageBuilder.BuildThumbCollage: a 960x540 PNG, the backdrop drawn
// darker, the library name in white at the centre, and a black image when no
// path decodes.
func TestThumbCollage(t *testing.T) {
	// libvips shutdown is terminal for a process; run in a fresh test binary.
	if os.Getenv(collageSubprocessMode) == "" {
		command := exec.Command(os.Args[0], "-test.run=^TestThumbCollage$", "-test.v")
		command.Env = append(os.Environ(), collageSubprocessMode+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("collage subprocess failed: %v\n%s", err, output)
		}
		return
	}
	requireNativeVips(t)
	runtimeOwner, err := images.OpenRuntime(t.Context())
	if err != nil {
		t.Fatalf("OpenRuntime(): %v", err)
	}
	t.Cleanup(func() { _ = runtimeOwner.Close() })
	builder := images.NewCollageBuilder(runtimeOwner)
	source := filepath.Join("testdata", "stock-corpus", "inputs", "landscape.jpg")
	missing := filepath.Join(t.TempDir(), "missing.jpg")

	decode := func(t *testing.T, data []byte) image.Image {
		t.Helper()
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode collage PNG: %v", err)
		}
		if got := decoded.Bounds(); got.Dx() != 960 || got.Dy() != 540 {
			t.Fatalf("collage size = %v, want 960x540", got)
		}
		return decoded
	}
	brightest := func(img image.Image, area image.Rectangle) uint32 {
		var best uint32
		for y := area.Min.Y; y < area.Max.Y; y++ {
			for x := area.Min.X; x < area.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				best = max(best, min(r, g, b))
			}
		}
		return best
	}

	t.Run("backdrop and name", func(t *testing.T) {
		data, err := builder.ThumbCollage(t.Context(), []string{missing, source}, 960, 540, "Movies & <TV>")
		if err != nil {
			t.Fatalf("ThumbCollage: %v", err)
		}
		img := decode(t, data)
		if got := brightest(img, image.Rect(400, 250, 560, 290)); got < 0xf000 {
			t.Errorf("centre brightest channel = %#x, want white text", got)
		}
		// The 0x78 black layer keeps every backdrop pixel at most 135/255.
		if got := brightest(img, image.Rect(0, 0, 960, 40)); got > 0x8800 {
			t.Errorf("top band brightest channel = %#x, want darkened backdrop", got)
		}
	})

	t.Run("no decodable image", func(t *testing.T) {
		data, err := builder.ThumbCollage(t.Context(), []string{missing}, 960, 540, "Movies")
		if err != nil {
			t.Fatalf("ThumbCollage: %v", err)
		}
		if got := brightest(decode(t, data), image.Rect(0, 0, 960, 540)); got != 0 {
			t.Errorf("brightest channel = %#x, want a black image", got)
		}
	})
}
