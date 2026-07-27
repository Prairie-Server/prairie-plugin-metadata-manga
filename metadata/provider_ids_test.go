package metadata

import "testing"

func TestProviderIDsFromMatchMergesExternalIDs(t *testing.T) {
	m := Match{
		Provider:   "mangabaka",
		ProviderID: "1677",
		ExternalIDs: map[string]string{
			"anilist":       "105778",
			"my_anime_list": "116778",
		},
	}
	ids := ProviderIDsFromMatch(m)
	if ids["mangabaka"] != "1677" {
		t.Fatalf("mangabaka id = %q, want 1677", ids["mangabaka"])
	}
	if ids["manga-metadata"] != "mangabaka:1677" {
		t.Fatalf("capability id = %q, want mangabaka:1677", ids["manga-metadata"])
	}
	if ids["anilist"] != "105778" || ids["my_anime_list"] != "116778" {
		t.Fatalf("external ids not merged: %v", ids)
	}
}

func TestParseCapabilityProviderID(t *testing.T) {
	src, id := ParseCapabilityProviderID(" mangabaka : 1677 ")
	if src != "mangabaka" || id != "1677" {
		t.Fatalf("%q %q", src, id)
	}
	if s, i := ParseCapabilityProviderID("nocolon"); s != "" || i != "" {
		t.Fatal(s, i)
	}
	if s, i := ParseCapabilityProviderID(":onlyid"); s != "" || i != "" {
		t.Fatal(s, i)
	}
	if s, i := ParseCapabilityProviderID("only:"); s != "" || i != "" {
		t.Fatal(s, i)
	}
}

func TestProviderIDsFromMatchSkipsEmpty(t *testing.T) {
	ids := ProviderIDsFromMatch(Match{
		Provider: "mangadex", ProviderID: "x",
		ExternalIDs: map[string]string{"": "1", "anilist": "", "mangadex": "dup"},
	})
	if ids["anilist"] != "" && ids[""] != "" {
		t.Fatal(ids)
	}
	if ids["mangadex"] != "x" {
		t.Fatal(ids)
	}
}
