// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/deadfin-org/deadfin/internal/api/handlers"
	"github.com/deadfin-org/deadfin/internal/api/router"
	"github.com/deadfin-org/deadfin/internal/auth"
	"github.com/deadfin-org/deadfin/internal/dto"
	"github.com/deadfin-org/deadfin/internal/images"
	"github.com/deadfin-org/deadfin/internal/imageserve"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/path"
)

const testItemHex = "7e897749137a8161f7a084cfad246bd3"

type fakeImageReader struct {
	infos []item.ImageInfo
	err   error
}

func (f fakeImageReader) Images(_ context.Context, _ dto.UUID) ([]item.ImageInfo, error) {
	return f.infos, f.err
}

type fakeRenderer struct {
	result     imageserve.Result
	err        error
	gotInfo    item.ImageInfo
	gotRequest imageserve.Request
	called     bool
}

func (f *fakeRenderer) Render(_ context.Context, info item.ImageInfo, request imageserve.Request) (imageserve.Result, error) {
	f.called = true
	f.gotInfo = info
	f.gotRequest = request
	return f.result, f.err
}

func writeArtifact(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.png")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func TestImageRoutesLedger(t *testing.T) {
	rt := router.New()
	handlers.RegisterImages(rt, fakeImageReader{}, &fakeRenderer{}, path.VirtualPathExpander{}, nil, nil)

	want := []router.Route{
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Images", OperationID: "GetItemImageInfos", Policy: string(auth.PolicyDefaultAuthorization)},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Images/{imageType}", OperationID: "GetItemImage", Anonymous: true},
		{Method: http.MethodHead, Pattern: "/Items/{itemId}/Images/{imageType}", OperationID: "HeadItemImage", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Images/{imageType}/{imageIndex}", OperationID: "GetItemImageByIndex", Anonymous: true},
		{Method: http.MethodHead, Pattern: "/Items/{itemId}/Images/{imageType}/{imageIndex}", OperationID: "HeadItemImageByIndex", Anonymous: true},
		{Method: http.MethodGet, Pattern: "/Items/{itemId}/Images/{imageType}/{imageIndex}/{tag}/{format}/{maxWidth}/{maxHeight}/{percentPlayed}/{unplayedCount}", OperationID: "GetItemImage2", Anonymous: true},
		{Method: http.MethodHead, Pattern: "/Items/{itemId}/Images/{imageType}/{imageIndex}/{tag}/{format}/{maxWidth}/{maxHeight}/{percentPlayed}/{unplayedCount}", OperationID: "HeadItemImage2", Anonymous: true},
	}
	for _, expected := range want {
		t.Run(expected.OperationID+"_"+expected.Method, func(t *testing.T) {
			got, found := routeByOperation(rt.Routes(), expected.OperationID)
			if !found {
				t.Fatalf("operation %q not registered", expected.OperationID)
			}
			if got.Method != expected.Method || got.Pattern != expected.Pattern || got.Policy != expected.Policy || got.Anonymous != expected.Anonymous {
				t.Fatalf("route %q = %#v, want %#v", expected.OperationID, got, expected)
			}
		})
	}
}

func TestServeItemImageWritesArtifactWithHeaders(t *testing.T) {
	artifact := writeArtifact(t, "PNG-BYTES")
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}}
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/png", Tag: "poster.jpg"}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary?width=320&quality=90&format=png", nil)
	rt.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := recorder.Header().Get("ETag"); got != `"poster.jpg"` {
		t.Errorf("ETag = %q, want quoted poster.jpg", got)
	}
	if recorder.Header().Get("Cache-Control") == "" {
		t.Error("Cache-Control not set")
	}
	if recorder.Body.String() != "PNG-BYTES" {
		t.Errorf("body = %q, want the artifact bytes", recorder.Body.String())
	}
	if !renderer.called {
		t.Fatal("renderer not invoked")
	}
	if renderer.gotRequest.Format == nil || *renderer.gotRequest.Format != images.FormatPNG {
		t.Errorf("request format = %v, want Png", renderer.gotRequest.Format)
	}
	if renderer.gotRequest.Resize.Width == nil || *renderer.gotRequest.Resize.Width != 320 {
		t.Errorf("request width = %v, want 320", renderer.gotRequest.Resize.Width)
	}
	if renderer.gotRequest.Quality != 90 || renderer.gotRequest.AutoOrient {
		t.Errorf("request quality/autoorient = %d/%v, want 90/false (item-orientation-resolved passthrough)", renderer.gotRequest.Quality, renderer.gotRequest.AutoOrient)
	}
}

func TestHeadItemImageOmitsBody(t *testing.T) {
	artifact := writeArtifact(t, "PNG-BYTES")
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}}
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/png", Tag: "poster.jpg"}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, "/Items/"+testItemHex+"/Images/Primary", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("HEAD body length = %d, want 0", recorder.Body.Len())
	}
	if recorder.Header().Get("ETag") != `"poster.jpg"` {
		t.Errorf("HEAD ETag = %q, want quoted poster.jpg", recorder.Header().Get("ETag"))
	}
}

func TestServeItemImageConditionalNotModified(t *testing.T) {
	artifact := writeArtifact(t, "PNG-BYTES")
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}}
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/png", Tag: "poster.jpg"}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary", nil)
	request.Header.Set("If-None-Match", `"poster.jpg"`)
	rt.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", recorder.Code)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("304 body length = %d, want 0", recorder.Body.Len())
	}
}

func TestServeItemImagePathEncodedMapsSegments(t *testing.T) {
	artifact := writeArtifact(t, "JPEG")
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Index: 0, Path: "/media/poster.jpg"}}}
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/jpeg", Tag: "poster.jpg"}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary/0/anytag/jpg/640/360/0/0", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if renderer.gotRequest.Format == nil || *renderer.gotRequest.Format != images.FormatJPEG {
		t.Errorf("format = %v, want Jpg", renderer.gotRequest.Format)
	}
	if renderer.gotRequest.Resize.MaxWidth == nil || *renderer.gotRequest.Resize.MaxWidth != 640 {
		t.Errorf("maxWidth = %v, want 640", renderer.gotRequest.Resize.MaxWidth)
	}
	if renderer.gotRequest.Resize.MaxHeight == nil || *renderer.gotRequest.Resize.MaxHeight != 360 {
		t.Errorf("maxHeight = %v, want 360", renderer.gotRequest.Resize.MaxHeight)
	}
}

func TestServeItemImagePathEncodedZeroBoundIsUnset(t *testing.T) {
	artifact := writeArtifact(t, "JPEG")
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}}
	renderer := &fakeRenderer{result: imageserve.Result{CachePath: artifact, ContentType: "image/jpeg", Tag: "poster.jpg"}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary/0/anytag/jpg/0/0/0/0", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if renderer.gotRequest.Resize.MaxWidth != nil || renderer.gotRequest.Resize.MaxHeight != nil {
		t.Errorf("zero bounds mapped to %v/%v, want both unset", renderer.gotRequest.Resize.MaxWidth, renderer.gotRequest.Resize.MaxHeight)
	}
}

func TestServeItemImageErrors(t *testing.T) {
	artifact := writeArtifact(t, "X")
	tests := []struct {
		name     string
		path     string
		reader   fakeImageReader
		renderer *fakeRenderer
		want     int
	}{
		{
			name:     "invalid image type",
			path:     "/Items/" + testItemHex + "/Images/Bogus",
			reader:   fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}},
			renderer: &fakeRenderer{result: imageserve.Result{CachePath: artifact}},
			want:     http.StatusBadRequest,
		},
		{
			name:     "no such image",
			path:     "/Items/" + testItemHex + "/Images/Logo",
			reader:   fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}},
			renderer: &fakeRenderer{result: imageserve.Result{CachePath: artifact}},
			want:     http.StatusNotFound,
		},
		{
			name:     "remote unavailable",
			path:     "/Items/" + testItemHex + "/Images/Primary",
			reader:   fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "https://cdn/x.jpg"}}},
			renderer: &fakeRenderer{err: imageserve.ErrRemoteUnavailable},
			want:     http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := router.New()
			handlers.RegisterImages(rt, test.reader, test.renderer, path.VirtualPathExpander{}, nil, nil)
			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}

func TestServeItemImageWithoutRendererIsUnavailable(t *testing.T) {
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: "/media/poster.jpg"}}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, nil, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no processor is wired", recorder.Code)
	}
}

func TestItemImageInfosListing(t *testing.T) {
	local := writeArtifact(t, "0123456789") // 10 bytes
	blur := "LKO2?U%2Tw=w]~RBVZRi};RPxuwH"
	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypePrimary, Index: 0, Path: local, Width: 320, Height: 180, BlurHash: &blur},
		{Type: item.ImageTypeBackdrop, Index: 0, Path: "https://cdn.example/backdrop.jpg"},
	}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, &fakeRenderer{}, path.VirtualPathExpander{}, nil, nil)

	recorder := httptest.NewRecorder()
	rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var listing []dto.ImageInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing) != 2 {
		t.Fatalf("listing length = %d, want 2", len(listing))
	}
	primary := listing[0]
	if primary.ImageType != "Primary" || primary.ImageTag != filepath.Base(local) {
		t.Errorf("primary type/tag = %q/%q, want Primary/%s", primary.ImageType, primary.ImageTag, filepath.Base(local))
	}
	if primary.ImageIndex == nil || *primary.ImageIndex != 0 {
		t.Errorf("primary index = %v, want 0", primary.ImageIndex)
	}
	if primary.Width == nil || *primary.Width != 320 || primary.Height == nil || *primary.Height != 180 {
		t.Errorf("primary dims = %v/%v, want 320/180", primary.Width, primary.Height)
	}
	if primary.BlurHash != blur {
		t.Errorf("primary blurhash = %q, want %q", primary.BlurHash, blur)
	}
	if primary.Size != 10 {
		t.Errorf("primary size = %d, want 10 (on-disk byte length)", primary.Size)
	}
	if backdrop := listing[1]; backdrop.ImageType != "Backdrop" || backdrop.ImageTag != "backdrop.jpg" || backdrop.Size != 0 {
		t.Errorf("backdrop = %+v, want Backdrop/backdrop.jpg/size 0 (remote, not downloaded)", backdrop)
	}
}

type fakeRemoteRemover struct {
	calls []string
}

func (f *fakeRemoteRemover) RemoveRemoteImage(_ context.Context, _ dto.UUID, imageType item.ImageType, path string) error {
	f.calls = append(f.calls, imageType.String()+" "+path)
	return nil
}

// TestServeItemImageRemoteFailures checks #886: a remote source that answers
// 404 or 403 removes the image and answers 404; another failure answers 503
// and keeps the image.
func TestServeItemImageRemoteFailures(t *testing.T) {
	const source = "https://cdn.example/logo.png"
	reader := fakeImageReader{infos: []item.ImageInfo{{Type: item.ImageTypePrimary, Path: source}}}
	for _, test := range []struct {
		name    string
		err     error
		want    int
		removed bool
	}{
		{"gone", imageserve.ErrRemoteGone, http.StatusNotFound, true},
		{"failed", imageserve.ErrRemoteFailed, http.StatusServiceUnavailable, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			remover := &fakeRemoteRemover{}
			rt := router.New()
			handlers.RegisterImages(rt, reader, &fakeRenderer{err: test.err}, path.VirtualPathExpander{}, remover, nil)
			recorder := httptest.NewRecorder()
			rt.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary?fillWidth=120", nil))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if got := len(remover.calls) == 1 && remover.calls[0] == "Primary "+source; got != test.removed {
				t.Fatalf("remover calls = %v, want removed=%v", remover.calls, test.removed)
			}
		})
	}
}
