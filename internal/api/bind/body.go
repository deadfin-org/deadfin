// SPDX-License-Identifier: GPL-2.0-only

package bind

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/deadfin-org/deadfin/internal/jsonnum"
)

// maxJSONBody guards against pathological payloads; upstream relies on
// Kestrel's default request limits (~28.6 MB). 32 MB keeps parity headroom.
const maxJSONBody = 32 << 20

// ErrUnsupportedMediaType is returned when a request body has an unsupported
// Content-Type.
var ErrUnsupportedMediaType = fmt.Errorf("unsupported media type")

// isJSONContentType accepts the three aliases the spec declares on request
// bodies: application/json, text/json, and application/*+json
// (75 operations each — 03-api-surface.md trap #4).
func isJSONContentType(ct string) bool {
	if ct == "" {
		// Clients (legacy apiclient included) sometimes omit the header on
		// JSON posts; upstream's formatter selection still parses them.
		return true
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "application/json", "text/json":
		return true
	}
	return strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")
}

// JSONBody decodes the request body into dst. Field-name matching is
// case-insensitive (Go default, matching System.Text.Json's
// PropertyNameCaseInsensitive=true upstream). Unknown fields are ignored.
// A numeric member also reads a JSON string that holds a number, as
// upstream's JsonNumberHandling.AllowReadingFromString does (jsonnum).
// An empty body binds nothing and returns nil (upstream treats absent bodies
// as all-null DTOs on the endpoints that allow them).
func JSONBody(r *http.Request, dst any) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return ErrUnsupportedMediaType
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return jsonnum.Unmarshal(body, dst)
}

// OptionalJSONBody decodes a body that an endpoint allows to be absent
// (EmptyBodyBehavior.Allow). present is false for an empty body, so the caller
// can tell an absent DTO from one whose members all default.
func OptionalJSONBody(r *http.Request, dst any) (present bool, err error) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return false, ErrUnsupportedMediaType
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody))
	if err != nil {
		return false, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return false, nil
	}
	return true, jsonnum.Unmarshal(body, dst)
}

// ImageBody validates an image Content-Type and returns a streaming decoder
// for its Base64-encoded request body. The media type is normalized to
// lowercase and excludes parameters.
func ImageBody(r *http.Request) (io.Reader, string, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", ErrUnsupportedMediaType
	}
	mediaType = strings.ToLower(mediaType)
	if !isKnownImageMediaType(mediaType) {
		return nil, "", ErrUnsupportedMediaType
	}

	return base64.NewDecoder(base64.StdEncoding, base64WhitespaceFilter{r: r.Body}), mediaType, nil
}

// isKnownImageMediaType mirrors MimeTypes 2.5.2's normalized image media
// types from MimeTypes.Nullable.cs.pp, plus Jellyfin's image/jpg,
// image/x-icon, and image/x-png aliases. A switch keeps the lookup static
// and allocation-free.
func isKnownImageMediaType(mediaType string) bool {
	switch mediaType {
	case "image/aces",
		"image/apng",
		"image/avci",
		"image/avcs",
		"image/avif",
		"image/bmp",
		"image/cgm",
		"image/dicom-rle",
		"image/dpx",
		"image/emf",
		"image/fits",
		"image/g3fax",
		"image/gif",
		"image/heic",
		"image/heic-sequence",
		"image/heif",
		"image/heif-sequence",
		"image/hej2k",
		"image/hsj2",
		"image/ief",
		"image/jls",
		"image/jp2",
		"image/jpeg",
		"image/jpg",
		"image/jph",
		"image/jphc",
		"image/jpm",
		"image/jpx",
		"image/jxr",
		"image/jxra",
		"image/jxrs",
		"image/jxs",
		"image/jxsc",
		"image/jxsi",
		"image/jxss",
		"image/ktx",
		"image/ktx2",
		"image/png",
		"image/prs.btif",
		"image/prs.pti",
		"image/sgi",
		"image/svg+xml",
		"image/t38",
		"image/tiff",
		"image/tiff-fx",
		"image/vnd.adobe.photoshop",
		"image/vnd.airzip.accelerator.azv",
		"image/vnd.dece.graphic",
		"image/vnd.djvu",
		"image/vnd.dwg",
		"image/vnd.dxf",
		"image/vnd.fastbidsheet",
		"image/vnd.fpx",
		"image/vnd.fst",
		"image/vnd.fujixerox.edmics-mmr",
		"image/vnd.fujixerox.edmics-rlc",
		"image/vnd.microsoft.icon",
		"image/vnd.ms-dds",
		"image/vnd.ms-modi",
		"image/vnd.ms-photo",
		"image/vnd.net-fpx",
		"image/vnd.pco.b16",
		"image/vnd.tencent.tap",
		"image/vnd.valve.source.texture",
		"image/vnd.wap.wbmp",
		"image/vnd.xiff",
		"image/vnd.zbrush.pcx",
		"image/webp",
		"image/wmf",
		"image/x-3ds",
		"image/x-cmu-raster",
		"image/x-cmx",
		"image/x-freehand",
		"image/x-icon",
		"image/x-jng",
		"image/x-mrsid-image",
		"image/x-pict",
		"image/x-png",
		"image/x-portable-anymap",
		"image/x-portable-bitmap",
		"image/x-portable-graymap",
		"image/x-portable-pixmap",
		"image/x-rgb",
		"image/x-tga",
		"image/x-xbitmap",
		"image/x-xpixmap",
		"image/x-xwindowdump":
		return true
	}
	return false
}

// base64WhitespaceFilter removes the whitespace tolerated by .NET's
// FromBase64Transform without buffering the request body.
type base64WhitespaceFilter struct {
	r io.Reader
}

func (r base64WhitespaceFilter) Read(p []byte) (int, error) {
	for {
		n, err := r.r.Read(p)
		if n == 0 {
			return 0, err
		}

		out := 0
		for _, b := range p[:n] {
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				p[out] = b
				out++
			}
		}
		if out != 0 || err != nil {
			return out, err
		}
	}
}

// ErrBodyTooLarge is returned when a request body is larger than the limit
// of its route.
var ErrBodyTooLarge = fmt.Errorf("request body too large")

// LimitedBody returns the raw request body, capped at limit bytes. It ports
// ASP.NET [RequestSizeLimit]: a declared Content-Length above limit returns
// ErrBodyTooLarge before any read, and a body that grows above limit while
// it streams fails the read with ErrBodyTooLarge.
func LimitedBody(w http.ResponseWriter, r *http.Request, limit int64) (io.Reader, error) {
	if r.ContentLength > limit {
		return nil, ErrBodyTooLarge
	}
	return limitedReader{r: http.MaxBytesReader(w, r.Body, limit)}, nil
}

type limitedReader struct{ r io.Reader }

func (l limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		err = ErrBodyTooLarge
	}
	return n, err
}
