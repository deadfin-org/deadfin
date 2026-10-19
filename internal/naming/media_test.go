// SPDX-License-Identifier: GPL-2.0-only

// Ported from upstream/jellyfin/tests/Jellyfin.Naming.Tests/{Music,AudioBook,Book,ExternalFiles}/*.cs.
package naming_test

import (
	"reflect"
	"testing"

	"github.com/deadfin-org/deadfin/internal/naming"
)

func mediaStringPtr(value string) *string { return &value }

// Jellyfin.Naming.Tests.Music.MultiDiscAlbumTests.AlbumParser_MultidiscPath_Identifies.
func TestIsMultiDiscAlbum(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"", false},
		{"C:/", false},
		{"/home/", false},
		{"/Music/Album/CD1", true},
		{"/Music/Album/Disc 2", true},
		{"/Music/Album/part 1", true},
		{"/Music/Beyoncé/I Am… Sasha Fierce (2008)/CD 01", true},
		{"/Music/Jimmy Eat World/Futures (2004)/Digital Media 01", true},
		{"/Music/Album/Disc 1", true},
		{"/Music/Album/Digital Media", false},
		{"Blah 04(Encores and Folk Songs)", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := naming.IsMultiDiscAlbum(tt.path, naming.DefaultOptions()); got != tt.want {
				t.Fatalf("IsMultiDiscAlbum(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

// Emby.Naming.Audio.AudioFileParser.IsAudioFile (no pinned upstream unit-test file).
func TestIsAudioFileUsesDefaultExtensionSet(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"track.mp3", true}, {"track.FLAC", true}, {"track.m4a", true}, {"track.mkv", false}, {"", false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			if got := naming.IsAudioFile(tt.path, naming.DefaultOptions()); got != tt.want {
				t.Fatalf("IsAudioFile(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

// Jellyfin.Naming.Tests.AudioBook.AudioBookResolverTests.Resolve_ValidFileName_Success,
// Resolve_InvalidExtension, and Resolve_EmptyFileName.
func TestResolveAudioBook(t *testing.T) {
	tests := []struct {
		path, container string
		chapter, part   *int
	}{
		{"/server/AudioBooks/Larry Potter/Larry Potter.mp3", "mp3", nil, nil},
		{"/server/AudioBooks/Berry Potter/Chapter 1 .ogg", "ogg", intp(1), nil},
		{"/server/AudioBooks/Nerry Potter/Part 3 - Chapter 2.mp3", "mp3", intp(2), intp(3)},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := naming.ResolveAudioBook(tt.path, naming.DefaultOptions())
			if err != nil {
				t.Fatalf("ResolveAudioBook(%q) error = %v", tt.path, err)
			}
			if got == nil || got.Path != tt.path || got.Container != tt.container || !reflect.DeepEqual(got.ChapterNumber, tt.chapter) || !reflect.DeepEqual(got.PartNumber, tt.part) {
				t.Fatalf("ResolveAudioBook(%q) = %#v, want container/chapter/part %q/%v/%v", tt.path, got, tt.container, tt.chapter, tt.part)
			}
		})
	}
	for _, path := range []string{"/server/AudioBooks/Larry Potter/Larry Potter.mp9", ""} {
		got, err := naming.ResolveAudioBook(path, naming.DefaultOptions())
		if err != nil {
			t.Fatalf("ResolveAudioBook(%q) error = %v", path, err)
		}
		if got != nil {
			t.Fatalf("ResolveAudioBook(%q) = %#v, want nil", path, got)
		}
	}
}

// Jellyfin.Naming.Tests.Book.BookResolverTests.Resolve_Books and Resolve_Comics.
func TestParseBook(t *testing.T) {
	tests := []struct {
		input               string
		name, series        *string
		index, parent, year *int
	}{
		{"Sherlock Holmes (1887) #1 (of 4) (1887)", nil, mediaStringPtr("Sherlock Holmes"), intp(1), nil, intp(1887)},
		{"Sherlock Holmes #2", nil, mediaStringPtr("Sherlock Holmes"), intp(2), nil, nil},
		{"A Study in Scarlet (Sherlock Holmes, #1) (1887)", mediaStringPtr("A Study in Scarlet"), mediaStringPtr("Sherlock Holmes"), intp(1), nil, intp(1887)},
		{"The Sign of the Four (1890)", mediaStringPtr("The Sign of the Four"), nil, nil, nil, intp(1890)},
		{"00 - Dracula's Guest (1914)", mediaStringPtr("Dracula's Guest"), nil, intp(0), nil, intp(1914)},
		{"2.1 - The Blockade Runners", mediaStringPtr("2.1 - The Blockade Runners"), nil, nil, nil, nil},
		{"Captain Marvel Adventures v01 (1941)", mediaStringPtr("Captain Marvel Adventures v01"), nil, nil, intp(1), intp(1941)},
		{"Captain Marvel Adventures c120", mediaStringPtr("Captain Marvel Adventures c120"), nil, intp(120), nil, nil},
		{"Captain Marvel Adventures v01 c120", mediaStringPtr("Captain Marvel Adventures v01 c120"), nil, intp(120), intp(1), nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := naming.ParseBook(tt.input)
			if !reflect.DeepEqual(got.Name, tt.name) || !reflect.DeepEqual(got.SeriesName, tt.series) || !reflect.DeepEqual(got.Index, tt.index) || !reflect.DeepEqual(got.ParentIndex, tt.parent) || !reflect.DeepEqual(got.Year, tt.year) {
				t.Fatalf("ParseBook(%q) = %#v, want name/series/index/parent/year %v/%v/%v/%v/%v", tt.input, got, tt.name, tt.series, tt.index, tt.parent, tt.year)
			}
		})
	}
}

// Jellyfin.Naming.Tests.ExternalFiles.ExternalPathParserTests.ParseFile_ExtraTokens_ParseToValues
// plus audio/subtitle extension acceptance cases.
func TestParseExternalSubtitleAndAudio(t *testing.T) {
	for _, tt := range []struct {
		path            string
		subtitle, audio bool
	}{
		{"MyVideo.ass", true, false},
		{"MyVideo.mks", true, false},
		{"MyVideo.sami", true, false},
		{"MyVideo.srt", true, false},
		{"MyVideo.vtt", true, false},
		{"MyVideo.aa", false, true},
		{"MyVideo.aac", false, true},
		{"MyVideo.flac", false, true},
		{"MyVideo.m4a", false, true},
		{"MyVideo.mp3", false, true},
		{"MyVideo.m4v", false, false},
		{"", false, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			if got := naming.ParseExternalSubtitle(tt.path, "", naming.DefaultOptions()); (got != nil) != tt.subtitle {
				t.Fatalf("ParseExternalSubtitle(%q) = %#v, want matched=%t", tt.path, got, tt.subtitle)
			}
			if got := naming.ParseExternalAudio(tt.path, "", naming.DefaultOptions()); (got != nil) != tt.audio {
				t.Fatalf("ParseExternalAudio(%q) = %#v, want matched=%t", tt.path, got, tt.audio)
			}
		})
	}
	tests := []struct {
		tokens                 string
		title, language        *string
		defaulted, forced, sdh bool
	}{
		{".default", nil, nil, true, false, false},
		{".forced", nil, nil, false, true, false},
		{".default.forced", nil, nil, true, true, false},
		{".en", nil, mediaStringPtr("eng"), false, false, false},
		{".fr.en", mediaStringPtr("fr"), mediaStringPtr("eng"), false, false, false},
		{".en.fr", mediaStringPtr("en"), mediaStringPtr("fre"), false, false, false},
		{".title.en.default.forced", mediaStringPtr("title"), mediaStringPtr("eng"), true, true, false},
		{".sdh.en.title", mediaStringPtr("title"), mediaStringPtr("eng"), false, false, true},
		{".en.cc.title", mediaStringPtr("title"), mediaStringPtr("eng"), false, false, true},
		{".Subs for Chinese Audio.eng", mediaStringPtr("Subs for Chinese Audio"), mediaStringPtr("eng"), false, false, false},
	}
	for _, tt := range tests {
		path := "My.Video" + tt.tokens + ".srt"
		t.Run(path, func(t *testing.T) {
			got := naming.ParseExternalSubtitle(path, tt.tokens, naming.DefaultOptions())
			if got == nil || !reflect.DeepEqual(got.Title, tt.title) || !reflect.DeepEqual(got.Language, tt.language) || got.IsDefault != tt.defaulted || got.IsForced != tt.forced || got.IsHearingImpaired != tt.sdh {
				t.Fatalf("ParseExternalSubtitle(%q) = %#v, want title/language/default/forced/sdh %v/%v/%t/%t/%t", path, got, tt.title, tt.language, tt.defaulted, tt.forced, tt.sdh)
			}
		})
	}
}
