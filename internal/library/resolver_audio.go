// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/deadfin-org/deadfin/internal/item"
	"github.com/deadfin-org/deadfin/internal/naming"
)

func resolveMusicArtist(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || !collectionIs(input, item.CollectionTypeMusic) || hasAncestorKind(input, item.KindMusicArtist, item.KindMusicAlbum) || !hasMusicAlbumChild(input.Children, input.ChildEntries, options) {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindMusicArtist)
		return &resolved, nil
	}
}

func resolveMusicAlbum(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(_ context.Context, input ResolveContext) (*item.Item, error) {
		if !input.Entry.IsDir || !collectionIs(input, item.CollectionTypeMusic) || hasAncestorKind(input, item.KindMusicAlbum) || !containsMusic(input.Children, input.ChildEntries, options) {
			return nil, nil
		}
		resolved := initializeContext(input, item.KindMusicAlbum)
		return &resolved, nil
	}
}

// hasMusicAlbumChild ports the directory loop of MusicArtistResolver.Resolve
// (MusicArtistResolver.cs:93-119). An artist subfolder or a child that is a
// music album makes the folder an artist. A multi-disc child folder is part
// of an album, so it does not.
func hasMusicAlbumChild(children []Entry, childEntries map[string][]Entry, options naming.NamingOptions) bool {
	for _, child := range children {
		if !child.IsDir {
			continue
		}
		name := filepath.Base(child.Path)
		for _, subfolder := range options.ArtistSubfolders {
			if strings.EqualFold(name, subfolder) {
				return true
			}
		}
		if naming.IsMultiDiscAlbum(child.Path, options) {
			continue
		}
		if containsMusic(childEntries[normalizedEntryPath(child.Path)], childEntries, options) {
			return true
		}
	}
	return false
}

// containsMusic ports MusicAlbumResolver.ContainsMusic with allowSubfolders
// set (MusicAlbumResolver.cs:128-179). A folder holds music when it has an
// audio file. Otherwise it holds music when it has at least one multi-disc
// subfolder ("CD 01", "Disc 2", "Digital Media 01") with audio files and no
// other subfolder with audio files.
func containsMusic(children []Entry, childEntries map[string][]Entry, options naming.NamingOptions) bool {
	if hasAudioChild(children, options) {
		return true
	}
	discFolders := 0
	for _, child := range children {
		if !child.IsDir || !hasAudioChild(childEntries[normalizedEntryPath(child.Path)], options) {
			continue
		}
		if !naming.IsMultiDiscAlbum(child.Path, options) {
			return false
		}
		discFolders++
	}
	return discFolders > 0
}

func hasAudioChild(children []Entry, options naming.NamingOptions) bool {
	for _, child := range children {
		if !child.IsDir && naming.IsAudioFile(child.Path, options) {
			return true
		}
	}
	return false
}

var numberedAudioTitle = regexp.MustCompile(`^\s*([0-9]+)\s*[-._]\s*(.+?)\s*$`)

func audioTitle(path string) (string, *int) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	match := numberedAudioTitle.FindStringSubmatch(name)
	if match == nil {
		return "", nil
	}
	index, err := strconv.Atoi(match[1])
	if err != nil || index < 1 || strings.TrimSpace(match[2]) == "" {
		return "", nil
	}
	return match[2], &index
}

func resolveAudio(options naming.NamingOptions) func(context.Context, ResolveContext) (*item.Item, error) {
	return func(ctx context.Context, input ResolveContext) (*item.Item, error) {
		if input.Entry.IsDir || fileExtension(input.Entry.Path) == ".cue" || !naming.IsAudioFile(input.Entry.Path, options) || input.CollectionType != item.CollectionTypeUnknown && !collectionIs(input, item.CollectionTypeMusic) && !collectionIs(input, item.CollectionTypeBooks) {
			return nil, nil
		}
		kind := item.KindAudio
		if collectionIs(input, item.CollectionTypeBooks) {
			kind = item.KindAudioBook
		}
		resolved := initializeContext(input, kind)
		if kind == item.KindAudio {
			if name, index := audioTitle(input.Entry.Path); name != "" {
				setName(&resolved, name)
				resolved.IndexNumber = index
			}
		}
		if collectionIs(input, item.CollectionTypeBooks) {
			book, err := naming.ResolveAudioBook(input.Entry.Path, options)
			if err != nil {
				return nil, err
			}
			if book != nil {
				name, err := naming.ParseAudioBookName(resolvedName(resolved), options)
				if err != nil {
					return nil, err
				}
				if name.Name != "" {
					setName(&resolved, name.Name)
				}
				resolved.IndexNumber = book.ChapterNumber
				resolved.ParentIndexNumber = book.PartNumber
				if book.Container != "" {
					container := book.Container
					resolved.Container = &container
				}
			}
		}
		return &resolved, nil
	}
}

func resolveAudioMany(options naming.NamingOptions) func(context.Context, ResolveContext) ([]*item.Item, error) {
	return func(ctx context.Context, input ResolveContext) ([]*item.Item, error) {
		if !collectionIs(input, item.CollectionTypeBooks) || len(input.Children) == 0 {
			return nil, nil
		}
		paths := make([]string, 0, len(input.Children))
		entries := make(map[string]Entry, len(input.Children))
		for _, entry := range input.Children {
			if !entry.IsDir && naming.IsAudioFile(entry.Path, options) {
				paths = append(paths, entry.Path)
				entries[entry.Path] = entry
			}
		}
		groups, err := naming.ResolveAudioBookList(paths, options)
		if err != nil {
			return nil, err
		}
		result := make([]*item.Item, 0, len(groups))
		for _, group := range groups {
			if len(group.Files) != 1 || len(group.Extras) != 0 || len(group.AlternateVersions) != 0 {
				continue
			}
			entryInput := input
			entryInput.Entry = entries[group.Files[0].Path]
			resolved, err := resolveAudio(options)(ctx, entryInput)
			if err != nil {
				return nil, err
			}
			result = append(result, resolved)
		}
		return result, nil
	}
}

func resolvedName(value item.Item) string {
	if value.Name == nil {
		return ""
	}
	return *value.Name
}

func hasAncestorKind(input ResolveContext, kinds ...item.Kind) bool {
	if input.Parent != nil {
		for _, kind := range kinds {
			if input.Parent.Kind == kind {
				return true
			}
		}
	}
	for _, ancestor := range input.Ancestors {
		if ancestor == nil {
			continue
		}
		for _, kind := range kinds {
			if ancestor.Kind == kind {
				return true
			}
		}
	}
	return false
}
