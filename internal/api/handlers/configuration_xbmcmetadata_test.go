// SPDX-License-Identifier: GPL-2.0-only

package handlers_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The xbmcmetadata named configuration is the NFO settings document
// (MediaBrowser.XbmcMetadata/Configuration/NfoConfigurationFactory.cs:13-23,
// MediaBrowser.Model/Configuration/XbmcMetadataOptions.cs).
func TestConfigurationXbmcMetadataDefaultsMatchUpstream(t *testing.T) {
	rt, _ := newConfigurationRouter(t, authenticatedConfigUser())
	rec := request(rt, http.MethodGet, "/System/Configuration/xbmcmetadata", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	want := map[string]any{
		"ReleaseDateFormat":            "yyyy-MM-dd",
		"SaveImagePathsInNfo":          true,
		"EnablePathSubstitution":       true,
		"EnableExtraThumbsDuplication": false,
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%q", err, rec.Body.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("xbmcmetadata = %#v, want %#v", got, want)
	}
}

func TestConfigurationXbmcMetadataPostPersists(t *testing.T) {
	rt, manager := newConfigurationRouter(t, adminConfigUser())
	// The key is case-insensitive, and omitted members keep their defaults.
	postJSONDocument(t, rt, "/System/Configuration/XbmcMetadata", map[string]any{
		"UserId":                       "6d1fb1e0b3e44c5f9a0c3a3c9cbd2c1a",
		"ReleaseDateFormat":            "dd.MM.yyyy",
		"EnableExtraThumbsDuplication": true,
	})
	rec := request(rt, http.MethodGet, "/System/Configuration/xbmcmetadata", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	want := map[string]any{
		"UserId":                       "6d1fb1e0b3e44c5f9a0c3a3c9cbd2c1a",
		"ReleaseDateFormat":            "dd.MM.yyyy",
		"SaveImagePathsInNfo":          true,
		"EnablePathSubstitution":       true,
		"EnableExtraThumbsDuplication": true,
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("xbmcmetadata = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(manager.Paths().Config, "xbmcmetadata.xml"))
	if err != nil {
		t.Fatalf("read xbmcmetadata.xml: %v", err)
	}
	for _, fragment := range []string{"<XbmcMetadataOptions>", "<ReleaseDateFormat>dd.MM.yyyy</ReleaseDateFormat>", "<UserId>6d1fb1e0b3e44c5f9a0c3a3c9cbd2c1a</UserId>"} {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("xbmcmetadata.xml lacks %q:\n%s", fragment, data)
		}
	}
}
