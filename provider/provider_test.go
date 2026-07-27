package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/prairie-server/prairie-plugin-metadata-manga/metadata"
)

type fakeSource struct {
	id      string
	matches []metadata.Match
	err     error
}

func (f *fakeSource) ID() string { return f.id }

func (f *fakeSource) Search(context.Context, metadata.SearchQuery) ([]metadata.Match, error) {
	return f.matches, f.err
}

func (f *fakeSource) Fetch(context.Context, string) (*metadata.Match, error) {
	if len(f.matches) == 0 {
		return nil, f.err
	}
	m := f.matches[0]
	return &m, f.err
}

// lifecycleSource is a fakeSource that also records Start()/Close() calls, used
// to assert Provider.Start/Close fan out to every source.
type lifecycleSource struct {
	fakeSource
	started bool
	closed  bool
}

func (l *lifecycleSource) Start()       { l.started = true }
func (l *lifecycleSource) Close() error { l.closed = true; return nil }

func TestProviderStartStartsSources(t *testing.T) {
	a := &lifecycleSource{fakeSource: fakeSource{id: "a"}}
	b := &lifecycleSource{fakeSource: fakeSource{id: "b"}}
	// A plain source without Start() must be skipped without panic.
	p := NewProviderWithSources([]Source{a, &fakeSource{id: "plain"}, b})

	p.Start()
	if !a.started || !b.started {
		t.Fatalf("Start did not fan out: a=%v b=%v", a.started, b.started)
	}
}

func TestProviderCloseClosesSources(t *testing.T) {
	a := &lifecycleSource{fakeSource: fakeSource{id: "a"}}
	b := &lifecycleSource{fakeSource: fakeSource{id: "b"}}
	p := NewProviderWithSources([]Source{a, &fakeSource{id: "plain"}, b})

	if err := p.Close(); err != nil {
		t.Fatalf("Close err = %v, want nil", err)
	}
	if !a.closed || !b.closed {
		t.Fatalf("Close did not fan out: a=%v b=%v", a.closed, b.closed)
	}
}

// A transient source failure (timeout, 429, network) must surface as a Search
// error: the host stamps an empty-success result as a terminal "no match",
// permanently excluding the item from future sweeps.
func TestSearchAllSourcesErroredReturnsError(t *testing.T) {
	wantErr := errors.New("anilist: transient HTTP 429")
	p := NewProviderWithSources([]Source{&fakeSource{id: "anilist", err: wantErr}})

	matches, err := p.Search(context.Background(), metadata.SearchQuery{Title: "X"})
	if err == nil {
		t.Fatalf("Search err = nil, want error wrapping %v", wantErr)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Search err = %v, want errors.Is(%v)", err, wantErr)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want empty", matches)
	}
}

// A genuine empty result set (sources answered, nothing matched) stays a
// non-error so the host can legitimately mark the item as having no match.
func TestSearchNoMatchesNoErrorsReturnsEmpty(t *testing.T) {
	p := NewProviderWithSources([]Source{&fakeSource{id: "anilist"}})

	matches, err := p.Search(context.Background(), metadata.SearchQuery{Title: "X"})
	if err != nil {
		t.Fatalf("Search err = %v, want nil", err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %v, want empty", matches)
	}
}

// If one source errors but another produced a match, the match wins: partial
// provider trouble must not block enrichment.
func TestSearchPartialErrorWithMatchSucceeds(t *testing.T) {
	match := metadata.Match{Provider: "anilist", ProviderID: "1", Title: "X"}
	p := NewProviderWithSources([]Source{
		&fakeSource{id: "broken", err: errors.New("boom")},
		&fakeSource{id: "anilist", matches: []metadata.Match{match}},
	})

	matches, err := p.Search(context.Background(), metadata.SearchQuery{Title: "X"})
	if err != nil {
		t.Fatalf("Search err = %v, want nil when a match exists", err)
	}
	if len(matches) != 1 || matches[0].ProviderID != "1" {
		t.Fatalf("matches = %v, want the anilist match", matches)
	}
}

// Sources are consulted in registration order: the first confident match
// wins and later sources are never reached.
func TestSearchPrefersFirstSourceMatch(t *testing.T) {
	first := metadata.Match{Provider: "anilist", ProviderID: "1", Title: "X"}
	second := metadata.Match{Provider: "mangadex", ProviderID: "uuid", Title: "X"}
	p := NewProviderWithSources([]Source{
		&fakeSource{id: "anilist", matches: []metadata.Match{first}},
		&fakeSource{id: "mangadex", matches: []metadata.Match{second}},
	})

	matches, err := p.Search(context.Background(), metadata.SearchQuery{Title: "X"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("Search = %v, %v; want one match", matches, err)
	}
	if matches[0].Provider != "anilist" {
		t.Fatalf("match provider = %q, want the first source", matches[0].Provider)
	}
}

func TestDefaultSourcesAreMangaBakaThenMangaDex(t *testing.T) {
	sources := defaultSources(Options{})
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
	if sources[0].ID() != "mangabaka" {
		t.Fatalf("first source = %q, want mangabaka", sources[0].ID())
	}
	if sources[1].ID() != "mangadex" {
		t.Fatalf("second source = %q, want mangadex", sources[1].ID())
	}
}

// A source with no confident match falls through to the next source.
func TestSearchFallsBackToSecondSource(t *testing.T) {
	second := metadata.Match{Provider: "mangadex", ProviderID: "uuid", Title: "X"}
	p := NewProviderWithSources([]Source{
		&fakeSource{id: "anilist"},
		&fakeSource{id: "mangadex", matches: []metadata.Match{second}},
	})

	matches, err := p.Search(context.Background(), metadata.SearchQuery{Title: "X"})
	if err != nil || len(matches) != 1 {
		t.Fatalf("Search = %v, %v; want fallback match", matches, err)
	}
	if matches[0].Provider != "mangadex" {
		t.Fatalf("match provider = %q, want the fallback source", matches[0].Provider)
	}
}

func TestNewProviderAndFetchRouting(t *testing.T) {
	p := NewProvider()
	if p == nil || len(p.sources) == 0 {
		t.Fatal("NewProvider")
	}
	_ = p.Close()

	opts := Options{EnabledSources: []string{"mangadex"}, DefaultRegion: "us", DisableAniListBanners: true}
	p2 := NewProviderWithOptions(opts)
	if len(p2.sources) != 1 || p2.sources[0].ID() != "mangadex" {
		t.Fatalf("filtered sources=%v", p2.sources)
	}
	_ = p2.Close()

	// unknown enabled list falls back to all
	p3 := NewProviderWithOptions(Options{EnabledSources: []string{"nope"}})
	if len(p3.sources) < 2 {
		t.Fatalf("fallback sources=%d", len(p3.sources))
	}
	_ = p3.Close()

	match := metadata.Match{Provider: "anilist", ProviderID: "9", Title: "X"}
	src := &fakeSource{id: "anilist", matches: []metadata.Match{match}}
	p4 := NewProviderWithSources([]Source{nil, &fakeSource{id: ""}, src})
	got, err := p4.Fetch(context.Background(), metadata.SearchQuery{
		ProviderIDs: map[string]string{"anilist": "9"},
	})
	if err != nil || got == nil || got.ProviderID != "9" {
		t.Fatalf("Fetch by source id %#v err=%v", got, err)
	}
	got2, err := p4.Fetch(context.Background(), metadata.SearchQuery{
		ProviderIDs: map[string]string{metadata.CapabilityID: "anilist:9"},
	})
	if err != nil || got2 == nil {
		t.Fatalf("Fetch by capability %#v err=%v", got2, err)
	}
	nilMatch, err := p4.Fetch(context.Background(), metadata.SearchQuery{})
	if err != nil || nilMatch != nil {
		t.Fatalf("empty fetch %v %v", nilMatch, err)
	}

	set := enabledSourceSet([]string{" MangaDex , mangabaka", "anilist"})
	if !set["mangadex"] || !set["mangabaka"] || !set["anilist"] {
		t.Fatalf("%v", set)
	}
}

func TestNewProviderWithSourcesSkipsNil(t *testing.T) {
	p := NewProviderWithSources(nil)
	if len(p.sources) != 0 {
		t.Fatal(p.sources)
	}
}
