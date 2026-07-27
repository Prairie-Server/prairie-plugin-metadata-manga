package main

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/prairie-server/prairie-plugin-sdk/pkg/pluginproto/prairie/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/prairie-server/prairie-plugin-metadata-manga/metadata"
	"github.com/prairie-server/prairie-plugin-metadata-manga/provider"
)

func configEntry(key, value string) *pluginv1.ConfigEntry {
	s, _ := structpb.NewStruct(map[string]any{"value": value})
	return &pluginv1.ConfigEntry{Key: key, Value: s}
}

func typedEntry(key string, value any) *pluginv1.ConfigEntry {
	s, _ := structpb.NewStruct(map[string]any{"value": value})
	return &pluginv1.ConfigEntry{Key: key, Value: s}
}

type stubSource struct {
	id      string
	matches []metadata.Match
	err     error
}

func (s *stubSource) ID() string { return s.id }
func (s *stubSource) Search(context.Context, metadata.SearchQuery) ([]metadata.Match, error) {
	return s.matches, s.err
}
func (s *stubSource) Fetch(_ context.Context, id string) (*metadata.Match, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.matches) == 0 {
		return nil, nil
	}
	m := s.matches[0]
	m.ProviderID = id
	return &m, nil
}

func mustStruct(t *testing.T, value map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// SWITCH/NUMBER controls deliver typed bool/number values (not strings).
func TestProviderOptionsFromConfigParsesTypedDumpKeys(t *testing.T) {
	opts := providerOptionsFromConfig([]*pluginv1.ConfigEntry{
		typedEntry("enable_local_dump", true),
		typedEntry("dump_path", "/mnt/dump"),
		typedEntry("dump_refresh_hours", float64(72)),
		typedEntry("enable_anilist_banners", false),
	})
	if !opts.EnableLocalDump {
		t.Fatalf("EnableLocalDump = false, want true")
	}
	if opts.DumpPath != "/mnt/dump" {
		t.Fatalf("DumpPath = %q", opts.DumpPath)
	}
	if opts.DumpRefreshHours != 72 {
		t.Fatalf("DumpRefreshHours = %d, want 72", opts.DumpRefreshHours)
	}
	if !opts.DisableAniListBanners {
		t.Fatalf("DisableAniListBanners = false, want true (banners disabled)")
	}
}

// String forms must still parse (defensive: hand-set values, older configs).
func TestProviderOptionsFromConfigParsesStringDumpKeys(t *testing.T) {
	opts := providerOptionsFromConfig([]*pluginv1.ConfigEntry{
		configEntry("enable_local_dump", "true"),
		configEntry("dump_refresh_hours", "72"),
	})
	if !opts.EnableLocalDump {
		t.Fatalf("EnableLocalDump = false, want true (string form)")
	}
	if opts.DumpRefreshHours != 72 {
		t.Fatalf("DumpRefreshHours = %d, want 72 (string form)", opts.DumpRefreshHours)
	}
}

func TestProviderOptionsFromConfigDefaults(t *testing.T) {
	opts := providerOptionsFromConfig(nil)
	if opts.EnableLocalDump {
		t.Fatalf("dump should default off")
	}
	if opts.DisableAniListBanners {
		t.Fatalf("banners should default on")
	}
}

func TestLoadManifestAndGetManifest(t *testing.T) {
	original := version
	version = "2.2.2-test"
	t.Cleanup(func() { version = original })
	manifest, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GetVersion() != "2.2.2-test" || len(manifest.GetChecksum()) != 64 {
		t.Fatalf("version/checksum = %q %q", manifest.GetVersion(), manifest.GetChecksum())
	}
	rs := &runtimeServer{manifest: manifest}
	resp, err := rs.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil || resp.GetManifest() != manifest {
		t.Fatalf("GetManifest err=%v", err)
	}
}

func TestLoadManifestEmbeddedVersion(t *testing.T) {
	original := version
	version = ""
	t.Cleanup(func() { version = original })
	manifest, err := loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.GetVersion() == "" || len(manifest.GetChecksum()) != 64 {
		t.Fatalf("version/checksum = %q/%q", manifest.GetVersion(), manifest.GetChecksum())
	}
}

func TestConfigureAndStateForRequest(t *testing.T) {
	rs := &runtimeServer{}
	state := rs.stateForRequest()
	if state.provider == nil {
		t.Fatal("default provider")
	}

	_, err := rs.Configure(context.Background(), &pluginv1.ConfigureRequest{
		Config: []*pluginv1.ConfigEntry{
			configEntry("enabled_sources", "mangadex"),
			configEntry("default_region", "us"),
			configEntry("enable_local_dump", "false"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state = rs.stateForRequest()
	if state.provider == nil || state.options.DefaultRegion != "us" {
		t.Fatalf("state=%#v", state)
	}
	if _, err := rs.Configure(context.Background(), &pluginv1.ConfigureRequest{
		Config: []*pluginv1.ConfigEntry{configEntry("default_region", "eu")},
	}); err != nil {
		t.Fatal(err)
	}
	state = rs.stateForRequest()
	if state.options.DefaultRegion != "eu" {
		t.Fatalf("reconfigured state=%#v", state)
	}
	_ = state.provider.Close()
}

func TestMetadataServerSearchAndGetMetadata(t *testing.T) {
	match := metadata.Match{
		Provider: "mangadex", ProviderID: "abc", Title: "One Piece", Description: "Pirates",
		PublishYear: 1997, CoverURL: "https://cdn.example.test/c.jpg", BannerURL: "https://cdn.example.test/b.jpg",
		Publisher: "Shueisha", Status: "ongoing", ContentRating: "safe", Language: "en",
		SeriesName: "OP", SeriesPosition: "1",
		Authors: []string{" Oda ", ""}, Genres: []string{" Action ", ""},
		ExternalIDs: map[string]string{"anilist": "21"},
	}
	p := provider.NewProviderWithSources([]provider.Source{&stubSource{id: "mangadex", matches: []metadata.Match{match}}})
	rs := &runtimeServer{state: runtimeState{provider: p, options: provider.Options{DefaultRegion: "jp"}}}
	ms := &metadataServer{runtime: rs}

	search, err := ms.Search(context.Background(), &pluginv1.SearchMetadataRequest{
		Query: "One Piece", ItemType: "manga", Language: "",
		ProviderIds: mustStruct(t, map[string]any{"extra": "1"}),
	})
	if err != nil || len(search.GetResults()) != 1 {
		t.Fatalf("Search %#v err=%v", search, err)
	}
	if search.GetResults()[0].GetProviderId() != "mangadex:abc" {
		t.Fatalf("provider id = %q", search.GetResults()[0].GetProviderId())
	}

	meta, err := ms.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{
		ProviderId: "abc", ItemType: "manga",
		ProviderIds: mustStruct(t, map[string]any{"mangadex": "abc"}),
	})
	if err != nil || meta.GetItem() == nil {
		t.Fatalf("GetMetadata %#v err=%v", meta, err)
	}
	item := meta.GetItem()
	if item.GetPosterPath() == "" || len(item.GetPeople()) != 1 || len(item.GetGenres()) != 1 {
		t.Fatalf("item=%#v", item)
	}
	if len(item.GetStudios()) != 1 || item.GetStudios()[0] != "Shueisha" {
		t.Fatalf("studios=%v", item.GetStudios())
	}

	empty, err := ms.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{ItemType: "manga"})
	if err != nil || empty.GetItem() != nil {
		t.Fatalf("empty %#v err=%v", empty, err)
	}

	errSrc := provider.NewProviderWithSources([]provider.Source{&stubSource{id: "mangadex", err: errors.New("boom")}})
	msErr := &metadataServer{runtime: &runtimeServer{state: runtimeState{provider: errSrc}}}
	if _, err := msErr.Search(context.Background(), &pluginv1.SearchMetadataRequest{Query: "x"}); err == nil {
		t.Fatal("expected search error")
	}
	if _, err := msErr.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{
		ProviderIds: mustStruct(t, map[string]any{"mangadex": "1"}),
	}); err == nil {
		t.Fatal("expected getmetadata error")
	}
}

func TestMainHelpers(t *testing.T) {
	if firstText("", " a ", "b") != "a" {
		t.Fatal("firstText")
	}
	if firstText("", " ") != "" {
		t.Fatal("empty firstText")
	}
	if stringMapFromStruct(nil) == nil {
		t.Fatal("nil map")
	}
	ids := providerIDsFromProto(mustStruct(t, map[string]any{"anilist": "1"}), capabilityID, "fallback")
	if ids[capabilityID] != "fallback" || ids["anilist"] != "1" {
		t.Fatal(ids)
	}
	if primaryProviderID(metadata.Match{}) != "" {
		t.Fatal("empty primary")
	}
	if publisherStudio("") != nil || publisherStudio(" P ")[0] != "P" {
		t.Fatal("studio")
	}
	if genresFromMatch(metadata.Match{Genres: []string{"", " "}}) != nil {
		t.Fatal("empty genres")
	}
	if peopleFromMatch(metadata.Match{Authors: []string{"", " "}}) != nil && len(peopleFromMatch(metadata.Match{})) != 0 {
		t.Fatal("people")
	}
	ms := metadataStruct(metadata.Match{Language: "en", SeriesName: "S", SeriesPosition: "1"})
	if ms.GetFields()["language"].GetStringValue() != "en" {
		t.Fatal(ms)
	}
	s, err := stringStruct(map[string]string{" a ": " 1 ", "": "x", "b": ""})
	if err != nil || s == nil {
		t.Fatal(s, err)
	}
	result, err := providerSearchResultFromMatch(metadata.Match{Provider: "mangabaka", ProviderID: "1", Title: "T"}, "manga")
	if err != nil || result == nil {
		t.Fatal(err)
	}
	item, err := metadataItemFromMatch(metadata.Match{Provider: "mangabaka", ProviderID: "1", Title: "T"}, "manga")
	if err != nil || item == nil {
		t.Fatal(err)
	}

	if configEntryString(nil) != "" {
		t.Fatal("nil string")
	}
	nilValueStruct := &structpb.Struct{Fields: map[string]*structpb.Value{"value": nil}}
	if configEntryString(nilValueStruct) != "" {
		t.Fatal("nil value string")
	}
	if configEntryString(mustStruct(t, map[string]any{"text": "via-text"})) != "via-text" {
		t.Fatal("text key")
	}
	if configEntryString(mustStruct(t, map[string]any{"other": "fallback"})) != "fallback" {
		t.Fatal("any string field")
	}
	if configEntryBoolDefault(nil, true) != true {
		t.Fatal("nil bool")
	}
	if !configEntryBoolDefault(nilValueStruct, true) {
		t.Fatal("nil value bool")
	}
	if !configEntryBoolDefault(mustStruct(t, map[string]any{"value": float64(1)}), false) {
		t.Fatal("number bool")
	}
	if configEntryBoolDefault(mustStruct(t, map[string]any{"value": "yes"}), false) != true {
		t.Fatal("yes")
	}
	if configEntryBoolDefault(mustStruct(t, map[string]any{"value": "off"}), true) != false {
		t.Fatal("off")
	}
	if configEntryBoolDefault(mustStruct(t, map[string]any{"value": "maybe"}), true) != true {
		t.Fatal("unrecognized keeps default")
	}
	if n, ok := configEntryNumber(nil); ok || n != 0 {
		t.Fatal("nil number")
	}
	if n, ok := configEntryNumber(nilValueStruct); ok || n != 0 {
		t.Fatal("nil value number")
	}
	if n, ok := configEntryNumber(mustStruct(t, map[string]any{"value": "12"})); !ok || n != 12 {
		t.Fatal("string number")
	}
	if _, ok := configEntryNumber(mustStruct(t, map[string]any{"value": "x"})); ok {
		t.Fatal("bad number")
	}
	opts := providerOptionsFromConfig([]*pluginv1.ConfigEntry{
		nil,
		configEntry("enabled_sources", "mangabaka, mangadex"),
		configEntry("default_region", "us"),
		{Key: "enable_local_dump", Value: mustStruct(t, map[string]any{"value": false})},
		{Key: "dump_refresh_hours", Value: mustStruct(t, map[string]any{"value": "0"})},
		{Key: "enable_anilist_banners", Value: mustStruct(t, map[string]any{"value": true})},
	})
	if len(opts.EnabledSources) != 1 || opts.DisableAniListBanners {
		t.Fatalf("opts=%#v", opts)
	}
}
