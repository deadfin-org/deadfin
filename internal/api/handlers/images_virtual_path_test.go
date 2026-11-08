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
	"github.com/deadfin-org/deadfin/internal/imageserve"
	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/path"
)

// fileRenderer reads the file at info.Path and serves it. It proves that the
// handler passes the expanded path to the renderer, not the raw token.
type fileRenderer struct {
	gotPath string
}

func (r *fileRenderer) Render(_ context.Context, info item.ImageInfo, _ imageserve.Request) (imageserve.Result, error) {
	r.gotPath = info.Path
	return imageserve.Result{CachePath: info.Path, ContentType: "image/jpeg", Tag: imageserve.Tag(info.Path)}, nil
}

// TestItemImageListingVirtualPathExpansion makes sure that the
// GET /Items/{id}/Images endpoint returns expanded paths in the listing DTO.
func TestItemImageListingVirtualPathExpansion(t *testing.T) {
	expander := path.NewVirtualPathExpander("/srv/data", "/srv/metadata")
	storedPath := "%MetadataPath%/library/backdrop.jpg"
	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypeBackdrop, Path: storedPath, Index: 0},
	}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, nil, expander, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Images status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var listing []struct {
		Path string `json:"Path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v; body=%s", err, rec.Body.String())
	}
	if len(listing) != 1 {
		t.Fatalf("listing length = %d, want 1", len(listing))
	}
	wantPath := "/srv/metadata/library/backdrop.jpg"
	if listing[0].Path != wantPath {
		t.Fatalf("listing path = %q, want %q (no raw token)", listing[0].Path, wantPath)
	}
	if listing[0].Path == storedPath {
		t.Fatal("listing path is the raw stored token, not expanded")
	}
	// ImageTag is asserted by TestItemImageListingPreservesImageTag with a
	// non-vacuous fixture. A prefix token keeps the same base name after
	// expansion, so a tag assertion here would not distinguish the sources.
}

// TestItemImageRenderVirtualPathExpansion makes sure that the image-serving
// path receives the expanded path before a local file is opened. The renderer
// reads a configured temporary file and the response contains its content.
func TestItemImageRenderVirtualPathExpansion(t *testing.T) {
	metadataDir := t.TempDir()
	expandedDir := filepath.Join(metadataDir, "library")
	if err := os.MkdirAll(expandedDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("poster-bytes")
	expandedPath := filepath.Join(expandedDir, "poster.jpg")
	if err := os.WriteFile(expandedPath, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	expander := path.NewVirtualPathExpander("/srv/data", metadataDir)
	storedPath := "%MetadataPath%/library/poster.jpg"
	renderer := &fileRenderer{}
	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: storedPath, Index: 0},
	}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, expander, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Image status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The renderer must receive the expanded path, not the raw token.
	if renderer.gotPath == storedPath {
		t.Fatalf("renderer received raw token path %q, want expanded", renderer.gotPath)
	}
	if renderer.gotPath != expandedPath {
		t.Fatalf("renderer path = %q, want %q", renderer.gotPath, expandedPath)
	}
	// The response must contain the content of the file at the expanded path.
	if rec.Body.String() != string(content) {
		t.Fatalf("response body = %q, want %q", rec.Body.String(), string(content))
	}
}

// TestItemImageListingNoTokenUnchanged makes sure that a stored path with no
// virtual token is returned unchanged through the listing endpoint.
func TestItemImageListingNoTokenUnchanged(t *testing.T) {
	expander := path.NewVirtualPathExpander("/srv/data", "/srv/metadata")
	storedPath := "/media/library/poster.jpg"
	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: storedPath, Index: 0},
	}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, nil, expander, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Images status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var listing []struct {
		Path string `json:"Path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v; body=%s", err, rec.Body.String())
	}
	if len(listing) != 1 {
		t.Fatalf("listing length = %d, want 1", len(listing))
	}
	if listing[0].Path != storedPath {
		t.Fatalf("listing path = %q, want unchanged %q", listing[0].Path, storedPath)
	}
}

// TestItemImageListingPreservesImageTag makes sure that ImageTag equals
// imageserve.Tag(info.Path) from the raw stored path while Path expands.
// The spec excludes image-tag changes.
func TestItemImageListingPreservesImageTag(t *testing.T) {
	// The stored path is the bare token so that the raw and expanded base
	// names differ. filepath.Base("%MetadataPath%") is "%MetadataPath%"
	// while filepath.Base("/srv/metadata") is "metadata". This makes the
	// tag assertion non-vacuous: if the handler derived the tag from the
	// expanded path, the tag would be "metadata", not "%MetadataPath%".
	expander := path.NewVirtualPathExpander("/srv/data", "/srv/metadata")
	storedPath := "%MetadataPath%"
	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: storedPath, Index: 0},
	}}
	rt := router.New()
	handlers.RegisterImages(rt, reader, nil, expander, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Images status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var listing []struct {
		Path     string `json:"Path"`
		ImageTag string `json:"ImageTag"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v; body=%s", err, rec.Body.String())
	}
	if len(listing) != 1 {
		t.Fatalf("listing length = %d, want 1", len(listing))
	}
	// Path must expand while ImageTag stays derived from the raw path.
	wantPath := "/srv/metadata"
	if listing[0].Path != wantPath {
		t.Fatalf("Path = %q, want expanded %q", listing[0].Path, wantPath)
	}
	wantTag := imageserve.Tag(storedPath)
	if wantTag == imageserve.Tag(wantPath) {
		t.Fatalf("test fixture is vacuous: raw tag %q equals expanded tag %q", wantTag, imageserve.Tag(wantPath))
	}
	if listing[0].ImageTag != wantTag {
		t.Fatalf("ImageTag = %q, want %q (from raw path, not expanded)", listing[0].ImageTag, wantTag)
	}
}

// TestItemImageRenderPreservesRawTag exercises both the listing and image
// serving endpoints with a stored path whose raw and expanded base names
// differ. It asserts that the listing Path and the renderer opened path
// equal the configured expanded path, and that the listing ImageTag and
// response ETag equal imageserve.Tag(storedPath), which differs from
// imageserve.Tag(expandedPath). This proves the external tag and path
// contracts without requiring a particular production implementation.
func TestItemImageRenderPreservesRawTag(t *testing.T) {
	// Create a real file at the expanded path so serveImageArtifact can
	// open it. The metadata root is a file, not a directory, so the bare
	// %MetadataPath% token expands to a path whose base name differs from
	// the raw base name "%MetadataPath%".
	metadataFile := filepath.Join(t.TempDir(), "cover.jpg")
	content := []byte("cover-bytes")
	if err := os.WriteFile(metadataFile, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	expander := path.NewVirtualPathExpander("/srv/data", metadataFile)
	storedPath := "%MetadataPath%"
	expandedPath := metadataFile

	// The raw and expanded base names must differ, or the tag assertion
	// is vacuous.
	wantTag := imageserve.Tag(storedPath)
	if wantTag == imageserve.Tag(expandedPath) {
		t.Fatalf("fixture is vacuous: raw tag %q equals expanded tag %q", wantTag, imageserve.Tag(expandedPath))
	}

	reader := fakeImageReader{infos: []item.ImageInfo{
		{Type: item.ImageTypePrimary, Path: storedPath, Index: 0},
	}}
	renderer := &fileRenderer{}
	rt := router.New()
	handlers.RegisterImages(rt, reader, renderer, expander, nil, nil)

	// Exercise the listing endpoint: GET /Items/{itemId}/Images.
	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images", nil)
	rt.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET Images status = %d, want 200; body=%s", listRec.Code, listRec.Body.String())
	}
	var listing []struct {
		Path     string `json:"Path"`
		ImageTag string `json:"ImageTag"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v; body=%s", err, listRec.Body.String())
	}
	if len(listing) != 1 {
		t.Fatalf("listing length = %d, want 1", len(listing))
	}
	if listing[0].Path != expandedPath {
		t.Fatalf("listing Path = %q, want expanded %q", listing[0].Path, expandedPath)
	}
	if listing[0].ImageTag != wantTag {
		t.Fatalf("listing ImageTag = %q, want %q (from raw path)", listing[0].ImageTag, wantTag)
	}

	// Exercise image serving: GET /Items/{itemId}/Images/Primary.
	serveRec := httptest.NewRecorder()
	serveReq := httptest.NewRequest(http.MethodGet, "/Items/"+testItemHex+"/Images/Primary", nil)
	rt.ServeHTTP(serveRec, serveReq)
	if serveRec.Code != http.StatusOK {
		t.Fatalf("GET Image status = %d, want 200; body=%s", serveRec.Code, serveRec.Body.String())
	}
	// The renderer must receive and open the expanded path, not the raw token.
	if renderer.gotPath != expandedPath {
		t.Fatalf("renderer path = %q, want expanded %q", renderer.gotPath, expandedPath)
	}
	if renderer.gotPath == storedPath {
		t.Fatal("renderer received the raw token path, not the expanded path")
	}
	// The response ETag must equal the raw-path tag, not the expanded-path tag.
	gotETag := serveRec.Header().Get("ETag")
	wantETag := `"` + wantTag + `"`
	if gotETag != wantETag {
		t.Fatalf("response ETag = %q, want %q (from raw path)", gotETag, wantETag)
	}
}
