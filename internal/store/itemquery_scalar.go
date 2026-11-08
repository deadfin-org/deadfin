// SPDX-License-Identifier: GPL-2.0-only

package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/deadfin-org/deadfin/internal/item"
)

// translateScalarFilters adds the predicates that are represented directly by
// an items column. Relation, media, and user-data predicates deliberately live
// with their respective query families.
//
// Source: jellyfin/jellyfin@b04614e18d2aeed8d3d3de920c381c6bd5b97bdb,
// Jellyfin.Server.Implementations/Item/BaseItemRepository.TranslateQuery.cs
// and BaseItemRepository.QueryBuilding.cs.
func (b *itemQueryFilterBuilder) translateScalarFilters(query ItemQuery) error {
	b.addScalarNameFilters(query)
	b.addScalarIdentityFilters(query)
	b.addScalarDateFilters(query)
	if err := b.addScalarStateFilters(query); err != nil {
		return err
	}
	b.addScalarNumericFilters(query)
	b.addScalarOwnershipFilters(query)
	return nil
}

func (b *itemQueryFilterBuilder) addScalarNameFilters(query ItemQuery) {
	if query.NameStartsWith != "" {
		b.clauses = append(b.clauses, b.d.SearchClause("sort_name", b.argument(b.d.EscapeLike(query.NameStartsWith)+"%")))
	}
	if query.NameStartsWithOrGreater != "" {
		b.clauses = append(b.clauses, "LOWER(sort_name) >= "+b.argument(strings.ToLower(query.NameStartsWithOrGreater)))
	}
	if query.NameLessThan != "" {
		b.clauses = append(b.clauses, "LOWER(sort_name) < "+b.argument(strings.ToLower(query.NameLessThan)))
	}
	if query.Name != "" {
		column := "clean_name"
		value := item.CleanName(query.Name)
		if query.UseRawName != nil && *query.UseRawName {
			column = "LOWER(name)"
			value = strings.ToLower(query.Name)
		}
		b.clauses = append(b.clauses, column+" = "+b.argument(value))
	}
	if query.NameContains != "" {
		clean := item.CleanName(query.NameContains)
		original := query.NameContains
		if !strings.ContainsAny(clean, "%_") {
			clean = b.d.EscapeLike(clean)
			original = b.d.EscapeLike(original)
		}
		clean = "%" + strings.Trim(clean, "%") + "%"
		original = "%" + strings.Trim(original, "%") + "%"
		b.clauses = append(b.clauses, "("+b.d.SearchClause("clean_name", b.argument(clean))+" OR "+b.d.SearchClause("original_title", b.argument(original))+")")
	}
}

func (b *itemQueryFilterBuilder) addScalarIdentityFilters(query ItemQuery) {
	b.addStringEquals("presentation_unique_key", query.PresentationUniqueKey)
	b.addStringEquals("path", query.Path)
	b.addStringEquals("external_id", query.ExternalID)
	b.addStringEquals("external_series_id", query.ExternalSeriesID)
	b.addStringEquals("series_presentation_unique_key", query.SeriesPresentationUniqueKey)
	if query.ParentType != nil {
		b.clauses = append(b.clauses, "EXISTS (SELECT 1 FROM items AS parent WHERE parent.id = items.parent_id AND parent.kind = "+b.argument(query.ParentType.String())+")")
	}
}

func (b *itemQueryFilterBuilder) addStringEquals(column, value string) {
	if value != "" {
		b.clauses = append(b.clauses, column+" = "+b.argument(value))
	}
}

func (b *itemQueryFilterBuilder) addScalarDateFilters(query ItemQuery) {
	b.addTimeLowerBound("start_date", query.MinStartDate)
	b.addTimeUpperBound("start_date", query.MaxStartDate)
	b.addTimeLowerBound("end_date", query.MinEndDate)
	b.addTimeUpperBound("end_date", query.MaxEndDate)
	b.addTimeLowerBound("date_created", query.MinDateCreated)
	b.addTimeLowerBound("date_last_saved", query.MinDateLastSaved)
}

func (b *itemQueryFilterBuilder) addTimeLowerBound(column string, value *time.Time) {
	if value != nil {
		b.clauses = append(b.clauses, column+" >= "+b.argument(b.d.TimeValue(value.UTC())))
	}
}

func (b *itemQueryFilterBuilder) addTimeUpperBound(column string, value *time.Time) {
	if value != nil {
		b.clauses = append(b.clauses, column+" <= "+b.argument(b.d.TimeValue(value.UTC())))
	}
}

func (b *itemQueryFilterBuilder) addScalarStateFilters(query ItemQuery) error {
	b.addBooleanFact("is_locked", query.IsLocked)
	if query.IsMovie != nil && (!*query.IsMovie || (len(query.IncludeItemTypes) != 0 && !itemQueryIncludesKind(query.IncludeItemTypes, item.KindMovie) && !itemQueryIncludesKind(query.IncludeItemTypes, item.KindTrailer))) {
		b.addBooleanFact("is_movie", query.IsMovie)
	}
	b.addBooleanFact("is_series", query.IsSeries)
	b.addBooleanFact("is_placeholder", query.IsPlaceHolder)

	if query.IsSpecialSeason != nil {
		predicate := "index_number = 0"
		if !*query.IsSpecialSeason {
			predicate = "(index_number <> 0 OR index_number IS NULL)"
		}
		b.clauses = append(b.clauses, "("+predicate+" AND "+b.boundBoolean(*query.IsSpecialSeason)+")")
	}
	if query.HasOverview != nil {
		predicate := "(overview IS NULL OR overview = '')"
		if *query.HasOverview {
			predicate = "(overview IS NOT NULL AND overview <> '')"
		}
		b.clauses = append(b.clauses, "("+predicate+" AND "+b.boundBoolean(*query.HasOverview)+")")
	}
	if query.HasOwnerID != nil {
		predicate := "owner_id IS NULL"
		if *query.HasOwnerID {
			predicate = "owner_id IS NOT NULL"
		}
		b.clauses = append(b.clauses, "("+predicate+" AND "+b.boundBoolean(*query.HasOwnerID)+")")
	}
	if query.IsAiring != nil || query.HasAired != nil {
		if query.ReferenceTime == nil || query.ReferenceTime.IsZero() {
			return fmt.Errorf("translate item query: airing filters require a reference time")
		}
		nowValue := b.d.TimeValue(query.ReferenceTime.UTC())
		now := b.argument(nowValue)
		if query.IsAiring != nil {
			if *query.IsAiring {
				b.clauses = append(b.clauses, b.airingClause(*query.ReferenceTime, nowValue, now))
			} else {
				b.clauses = append(b.clauses, "start_date > "+now+" AND end_date < "+b.repeatedArgument(nowValue, now))
			}
		}
		if query.HasAired != nil {
			operator := ">"
			if *query.HasAired {
				operator = "<="
			}
			hasAiredAt := now
			if query.IsAiring != nil {
				hasAiredAt = b.repeatedArgument(nowValue, now)
			}
			b.clauses = append(b.clauses, "end_date "+operator+" "+hasAiredAt)
		}
	}
	return nil
}

// airingSpanLimits returns the upper span limits of the airing classes. A
// programme whose span is longer than the last limit, or which has no
// span, is in the last class, len(airingSpanLimits()).
func airingSpanLimits() []time.Duration {
	return []time.Duration{
		time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour,
		24 * time.Hour, 48 * time.Hour, 7 * 24 * time.Hour,
	}
}

// AiringClass returns the airing class expression of d. The airing index
// ix_items_airing (migration 00033) keys on this expression and start_date;
// the migrations hold the same text.
func AiringClass(d Dialect) string {
	limits := airingSpanLimits()
	var class strings.Builder
	class.WriteString("(CASE")
	for index, limit := range limits {
		fmt.Fprintf(&class, " WHEN %s THEN %d", d.SpanAtMost("start_date", "end_date", limit), index)
	}
	fmt.Fprintf(&class, " ELSE %d END)", len(limits))
	return class.String()
}

// airingClause matches start_date <= now AND end_date >= now. It bounds
// the start of an airing item by its span class as well: an item with a
// span of at most limit airs at now only if it started at or after now -
// limit. ix_items_airing then reads in each class only the recent starts,
// not every item that started before now, which includes every past
// programme of the guide (#892). Every arm bounds start_date, so each arm
// implies the partial index predicate. The clause selects ids in a
// subquery, so the planner cannot walk ix_items_top_kind_start for the
// airing range instead. now is the placeholder of nowValue.
func (b *itemQueryFilterBuilder) airingClause(reference time.Time, nowValue any, now string) string {
	class, limits := AiringClass(b.d), airingSpanLimits()
	arms := make([]string, 0, len(limits)+1)
	// The first placeholder in the text is now, which the caller bound.
	at := now
	for index := range len(limits) + 1 {
		arm := fmt.Sprintf("(start_date <= %s AND %s = %d", at, class, index)
		if index < len(limits) {
			arm += " AND start_date >= " + b.argument(b.d.TimeValue(reference.UTC().Add(-limits[index])))
		}
		arms = append(arms, arm+")")
		at = b.repeatedArgument(nowValue, now)
	}
	return "id IN (SELECT id FROM items WHERE (" + strings.Join(arms, " OR ") + ") AND end_date >= " + at + ")"
}

func (b *itemQueryFilterBuilder) boundBoolean(value bool) string {
	return b.argument(boolArg(b.d, value)) + " = " + b.d.Boolean(value)
}

// repeatedArgument preserves a numbered parameter where a dialect permits
// reuse and binds a fresh value for every anonymous placeholder occurrence.
func (b *itemQueryFilterBuilder) repeatedArgument(value any, placeholder string) string {
	if b.d.Placeholder(1) != b.d.Placeholder(2) {
		return placeholder
	}
	return b.argument(value)
}

func (b *itemQueryFilterBuilder) addScalarNumericFilters(query ItemQuery) {
	b.addIntegerLowerBound("index_number", query.MinIndexNumber)
	b.addIntegerEquals("index_number", query.IndexNumber)
	b.addIntegerEquals("parent_index_number", query.ParentIndexNumber)
	if query.ParentIndexNumberNotEquals != nil {
		b.clauses = append(b.clauses, "(parent_index_number <> "+b.argument(*query.ParentIndexNumberNotEquals)+" OR parent_index_number IS NULL)")
	}
	if query.MinParentAndIndexNumber != nil {
		cursor := query.MinParentAndIndexNumber
		greaterParent := b.argument(cursor.ParentIndexNumber)
		equalParent := b.argument(cursor.ParentIndexNumber)
		index := b.argument(cursor.IndexNumber)
		b.clauses = append(b.clauses, "(parent_index_number > "+greaterParent+" OR (parent_index_number = "+equalParent+" AND index_number >= "+index+"))")
	}
	if query.AiredDuringSeason != nil {
		if *query.AiredDuringSeason < 1 {
			b.clauses = append(b.clauses, "parent_index_number = "+b.argument(*query.AiredDuringSeason))
		} else {
			value := b.argument(*query.AiredDuringSeason)
			b.clauses = append(b.clauses, "(parent_index_number = "+value+" OR airs_after_season_number = "+b.repeatedArgument(*query.AiredDuringSeason, value)+" OR airs_before_season_number = "+b.repeatedArgument(*query.AiredDuringSeason, value)+")")
		}
	}
	b.addIntegerLowerBound("width", query.MinWidth)
	b.addIntegerUpperBound("width", query.MaxWidth)
	b.addIntegerLowerBound("height", query.MinHeight)
	b.addIntegerUpperBound("height", query.MaxHeight)
}

func (b *itemQueryFilterBuilder) addIntegerLowerBound(column string, value *int) {
	if value != nil {
		b.clauses = append(b.clauses, column+" >= "+b.argument(*value))
	}
}

func (b *itemQueryFilterBuilder) addIntegerUpperBound(column string, value *int) {
	if value != nil {
		b.clauses = append(b.clauses, column+" <= "+b.argument(*value))
	}
}

func (b *itemQueryFilterBuilder) addIntegerEquals(column string, value *int) {
	if value != nil {
		b.clauses = append(b.clauses, column+" = "+b.argument(*value))
	}
}

func (b *itemQueryFilterBuilder) addScalarOwnershipFilters(query ItemQuery) {
	if len(query.ChannelIDs) != 0 {
		b.clauses = append(b.clauses, "channel_id IN ("+b.uuidList(query.ChannelIDs)+")")
	}
	if len(query.OwnerIDs) != 0 {
		b.clauses = append(b.clauses, "owner_id IN ("+b.uuidList(query.OwnerIDs)+")")
	}
	if len(query.ExtraTypes) != 0 {
		values := make([]string, len(query.ExtraTypes))
		for i, value := range query.ExtraTypes {
			values[i] = enum(value)
		}
		b.clauses = append(b.clauses, "extra_type IN ("+b.stringList(values)+")")
	}
	// Ordinary candidate rows omit alternate-version children before count and
	// paging. Direct retrieval and access-filtered playback source checks set
	// IncludeOwnedItems to bypass this ordinary-candidate filter.
	if len(query.OwnerIDs) == 0 && len(query.ExtraTypes) == 0 && !query.IncludeOwnedItems && query.HasOwnerID == nil {
		const ownedOrExtra = "(owner_id IS NULL OR extra_type IS NOT NULL)"
		const linkedAlternate = "NOT EXISTS (SELECT 1 FROM linked_children AS alternate_link JOIN items AS alternate_parent ON alternate_parent.id = alternate_link.parent_item_id WHERE alternate_link.child_item_id = items.id AND alternate_link.type IN ('LocalAlternateVersion', 'LinkedAlternateVersion') AND alternate_parent.kind <> 'BoxSet')"
		const ordinaryCandidate = "primary_version_id IS NULL AND " + linkedAlternate + " AND " + ownedOrExtra
		if query.IsResumable != nil && *query.IsResumable {
			b.clauses = append(b.clauses, ownedOrExtra)
			return
		}
		b.clauses = append(b.clauses, ordinaryCandidate)
	}
}
