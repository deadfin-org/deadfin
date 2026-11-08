// SPDX-License-Identifier: GPL-2.0-only

package guide

import (
	"regexp"
	"strconv"
	"strings"
)

// IPTV guides often have no <episode-num> or <sub-title>. They put the
// episode in the first description line ("S07 E17 The Doll") and mark a new
// airing with a superscript word after the title ("The Simpsons  ᴺᵉʷ").
// Upstream takes this text literally; Deadfin reads it when the listing
// provider allows it (D-123, deadfin#730).

// descriptionEpisode matches the first description line.
var descriptionEpisode = regexp.MustCompile(`^S(\d+)\s*E(\d+)\s*(.*)$`)

// newMarker is the superscript "New" after a programme title.
const newMarker = "ᴺᵉʷ"

// readDescriptionEpisode sets the episode facts that p carries only in its
// title and description text. It does not change a programme that has an
// <episode-num> or a <sub-title>.
func readDescriptionEpisode(p XMLTVProgramme) XMLTVProgramme {
	if p.Title != nil {
		if title, ok := strings.CutSuffix(strings.TrimRight(*p.Title, " \t"), newMarker); ok {
			title = strings.TrimRight(title, " \t")
			p.Title = &title
			p.IsNew = true
		}
	}
	if p.HasEpisode || p.Description == nil {
		return p
	}
	first, rest, _ := strings.Cut(*p.Description, "\n")
	match := descriptionEpisode.FindStringSubmatch(strings.TrimSpace(first))
	if match == nil {
		return p
	}
	season, err := strconv.Atoi(match[1])
	if err != nil {
		return p
	}
	episode, err := strconv.Atoi(match[2])
	if err != nil {
		return p
	}
	if season > maxDescriptionSeason || episode > maxDescriptionEpisode {
		return p
	}
	p.HasEpisode = true
	p.Series = &season
	p.Episode = &episode
	remainder := strings.TrimSpace(match[3])
	overview := strings.TrimSpace(rest)
	if overview == "" && remainder != "" && !looksLikeTitle(remainder) {
		// Single-line form with a synopsis after the numbers.
		p.Description = &remainder
		return p
	}
	if remainder != "" {
		p.EpisodeTitle = &remainder
	}
	if overview != "" {
		p.Description = &overview
	}
	return p
}

// Numbers above these limits are not season and episode numbers.
const (
	maxDescriptionSeason  = 1000
	maxDescriptionEpisode = 9999
)

// maxTitleWords is the longest text that counts as an episode title.
const maxTitleWords = 8

// looksLikeTitle reports whether s is short and has no sentence end or list
// separator before its last character.
func looksLikeTitle(s string) bool {
	if len(strings.Fields(s)) > maxTitleWords {
		return false
	}
	return !strings.ContainsAny(s[:len(s)-1], ".!?;…")
}
