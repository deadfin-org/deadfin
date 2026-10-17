// SPDX-License-Identifier: GPL-2.0-only

package library

import (
	"testing"

	"github.com/deadfin-org/deadfin/internal/item"
)

// TestNFOPeopleCarriesProviderPersonImage covers LibraryManager.UpdatePeopleAsync:
// a credit's provider image becomes its Person entity's Primary image, the
// first credit with an image wins, and the credit no longer carries the URL.
func TestNFOPeopleCarriesProviderPersonImage(t *testing.T) {
	const first, second = "https://image.tmdb.org/t/p/original/a.jpg", "https://image.tmdb.org/t/p/original/b.jpg"
	alice, bob := PersonEntity("Alice"), PersonEntity("Bob")
	values := []item.Item{
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice"}, {PersonID: bob.ID, Name: "Bob"}}},
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ImageURL: first}}},
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ImageURL: second}}},
	}
	people := nfoPeople(values)
	clearCreditImageURLs(values)

	byID := make(map[string]item.Item, len(people))
	for _, person := range people {
		byID[person.ID.String()] = person
	}
	if got := byID[alice.ID.String()].ImageInfos; len(got) != 1 || got[0].Type != item.ImageTypePrimary || got[0].Path != first {
		t.Fatalf("Alice images = %#v, want the first credited Primary image", got)
	}
	if got := byID[bob.ID.String()].ImageInfos; len(got) != 0 {
		t.Fatalf("Bob images = %#v, want none", got)
	}
	for _, value := range values {
		for _, credit := range value.People {
			if credit.ImageURL != "" {
				t.Fatalf("credit %q kept ImageURL %q after the scan moved it", credit.Name, credit.ImageURL)
			}
		}
	}
}

// TestNFOPeopleCarriesProviderPersonIDs covers SavePeopleMetadataAsync: each
// credit's PersonInfo.ProviderIds value is set on its Person entity, and the
// credit no longer carries the ids.
func TestNFOPeopleCarriesProviderPersonIDs(t *testing.T) {
	alice := PersonEntity("Alice")
	var tmdb, imdb item.ProviderIDs
	tmdb.Set("Tmdb", "131")
	imdb.Set("Imdb", "nm0350453")
	imdb.Set("Tmdb", "")
	values := []item.Item{
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ProviderIDs: tmdb}}},
		{People: []item.PersonCredit{{PersonID: alice.ID, Name: "Alice", ProviderIDs: imdb}}},
	}
	people := nfoPeople(values)
	clearCreditImageURLs(values)

	if len(people) != 1 {
		t.Fatalf("people = %#v, want Alice", people)
	}
	if got, _ := people[0].ProviderIDs.Get("Tmdb"); got != "131" {
		t.Errorf("Alice Tmdb id = %q, want 131 (an empty credit value does not clear it)", got)
	}
	if got, _ := people[0].ProviderIDs.Get("Imdb"); got != "nm0350453" {
		t.Errorf("Alice Imdb id = %q, want nm0350453", got)
	}
	for _, value := range values {
		for _, credit := range value.People {
			if len(credit.ProviderIDs.Snapshot()) != 0 {
				t.Fatalf("credit %q kept provider ids %v after the scan moved them", credit.Name, credit.ProviderIDs.Snapshot())
			}
		}
	}
}
