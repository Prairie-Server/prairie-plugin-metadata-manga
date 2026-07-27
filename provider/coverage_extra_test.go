package provider

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"github.com/prairie-server/prairie-plugin-metadata-manga/metadata"
)

func TestDumpBackendFetchByIDAndPaths(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "series.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":42,"title":"Fetch Me","type":"manga"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := buildDumpIndex(context.Background(), jsonlPath, filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_ = idx.close()

	b := newDumpBackend(dir, 0) // default refresh hours
	if b.refreshHours != 168 {
		t.Fatalf("refreshHours = %d", b.refreshHours)
	}
	_ = b.jsonlPath()
	if !b.openExisting() {
		t.Fatal("openExisting")
	}
	got, err := b.fetch(context.Background(), "42")
	if err != nil || got == nil || got.ID != 42 {
		t.Fatalf("fetch %#v err=%v", got, err)
	}
	miss, err := b.fetch(context.Background(), "999")
	if err != nil || miss != nil {
		t.Fatalf("miss %#v err=%v", miss, err)
	}
	empty, err := b.fetch(context.Background(), "  ")
	if err != nil || empty != nil {
		t.Fatalf("empty %#v err=%v", empty, err)
	}
	nilBackend := newDumpBackend(t.TempDir(), 1)
	none, err := nilBackend.fetch(context.Background(), "1")
	if err != nil || none != nil {
		t.Fatalf("unready %#v err=%v", none, err)
	}
	b.stop()
}

func TestDumpRefreshIfNeededDownloads(t *testing.T) {
	raw := []byte(`{"id":7,"title":"Refreshed","type":"manga"}` + "\n")
	var zbuf bytes.Buffer
	enc, _ := zstd.NewWriter(&zbuf)
	_, _ = enc.Write(raw)
	_ = enc.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zbuf.Bytes())
	}))
	defer srv.Close()

	prev := dumpDownloadURL
	dumpDownloadURL = srv.URL
	t.Cleanup(func() { dumpDownloadURL = prev })

	dir := t.TempDir()
	b := newDumpBackend(dir, 1)
	b.refreshIfNeeded(context.Background())
	if !b.ready() {
		t.Fatal("expected ready after refresh")
	}
	got, err := b.search(context.Background(), "Refreshed")
	if err != nil || len(got) != 1 || got[0].ID != 7 {
		t.Fatalf("search %#v err=%v", got, err)
	}
	// Fresh index should skip re-download.
	b.refreshIfNeeded(context.Background())
}

func TestDownloadAndDecompressHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	err := downloadAndDecompress(context.Background(), srv.URL, filepath.Join(t.TempDir(), "out.jsonl"))
	if err == nil {
		t.Fatal("expected status error")
	}
}

func TestBuildDumpIndexSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	jsonl := "\n" + `not-json` + "\n" + `{"id":3,"title":"Ok","type":"manga"}` + "\n"
	path := filepath.Join(dir, "series.jsonl")
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := buildDumpIndex(context.Background(), path, filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.close()
	got, err := idx.fetchByID(context.Background(), "3")
	if err != nil || got == nil || got.Title != "Ok" {
		t.Fatalf("%#v err=%v", got, err)
	}
}

func TestIsStaleMissingBuiltAt(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "series.jsonl")
	dbPath := filepath.Join(dir, "index.sqlite")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":1,"title":"X","type":"manga"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := buildDumpIndex(context.Background(), jsonlPath, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = idx.close()
	// Index handles are read-only; mutate via a writable connection.
	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`UPDATE _meta SET value = 'not-a-time' WHERE key = ?`, metaBuiltAtKey); err != nil {
		t.Fatal(err)
	}
	_ = rw.Close()
	idx, err = openDumpIndex(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.close()
	b := newDumpBackend(dir, 168)
	b.index = idx
	if !b.isStale() {
		t.Fatal("unparseable built_at should be stale")
	}
}

func TestMangaDexFetchByIDAndRetry(t *testing.T) {
	body := `{"result":"ok","data":{"id":"uuid-fetch","attributes":{
		"title":{"ja":"タイトル","en":""},"altTitles":[],"description":{"en":"d"},
		"year":2020,"status":"ongoing","tags":[]},"relationships":[]}}`
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s := NewMangaDexSource(Options{})
	s.endpoint = srv.URL
	got, err := s.Fetch(context.Background(), "mangadex:uuid-fetch")
	if err != nil || got == nil || got.ProviderID != "uuid-fetch" {
		t.Fatalf("Fetch %#v err=%v", got, err)
	}
	if got.Title != "タイトル" {
		t.Fatalf("fallback title = %q", got.Title)
	}
	empty, err := s.Fetch(context.Background(), "mangadex:")
	if err != nil || empty != nil {
		t.Fatalf("empty id %#v err=%v", empty, err)
	}
}

func TestMangaDexHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	s := NewMangaDexSource(Options{})
	s.endpoint = srv.URL
	if _, err := s.Search(context.Background(), metadata.SearchQuery{Title: "X"}); err == nil {
		t.Fatal("expected HTTP error")
	}
}

func TestAniListQueryRetryAndParse(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"Media":{"id":9,"bannerImage":"https://b.jpg"}}}`))
	}))
	defer srv.Close()
	media, err := fetchAniListByID(context.Background(), srv.Client(), srv.URL, 9)
	if err != nil || media == nil || media.ID != 9 {
		t.Fatalf("%#v err=%v", media, err)
	}
	if _, err := parseAniListByID([]byte(`{"errors":[{"message":"nope"}]}`)); err == nil {
		t.Fatal("expected graphql error")
	}
	if _, err := parseAniListByID([]byte(`not-json`)); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestNormalizeTitleAndPartNumber(t *testing.T) {
	if normalizeTitle("Part 01") != normalizeTitle("Part 1") {
		t.Fatal("leading zeros")
	}
	if normalizeTitle("Chapter 0") != "chapter0" {
		t.Fatal(normalizeTitle("Chapter 0"))
	}
	if partNumber("No Part") != "" || partNumber("X Part 3 Y") != "3" {
		t.Fatal(partNumber("X Part 3 Y"))
	}
}

func TestLiveBackendReadyAndTypesHelpers(t *testing.T) {
	if !newLiveBackend().ready() {
		t.Fatal("live ready")
	}
	if mangaBakaChapterCount(mangaBakaSeries{}) != 0 {
		t.Fatal("empty chapters")
	}
	if mangaBakaChapterCount(mangaBakaSeries{TotalChapters: "12"}) != 12 {
		t.Fatal("int chapters")
	}
	if mangaBakaChapterCount(mangaBakaSeries{TotalChapters: "3.9"}) != 3 {
		t.Fatal("float chapters")
	}
	if mangaBakaChapterCount(mangaBakaSeries{TotalChapters: "x"}) != 0 {
		t.Fatal("bad chapters")
	}
	pubs := []mangaBakaPublisher{{Name: "JP Pub", Type: "Japanese"}, {Name: "EN Pub", Type: "English"}}
	if firstPublisherName(pubs) != "EN Pub" {
		t.Fatal(firstPublisherName(pubs))
	}
	if firstPublisherName([]mangaBakaPublisher{{Name: "", Type: "English"}, {Name: "Only"}}) != "Only" {
		t.Fatal("fallback")
	}
	if firstNonEmpty("", " a ") != "a" {
		t.Fatal("firstNonEmpty")
	}
}

func TestMangaBakaSourceStartCloseWithDump(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "series.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":1,"title":"Naruto","type":"manga"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := buildDumpIndex(context.Background(), jsonlPath, filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_ = idx.close()

	src := NewMangaBakaSource(Options{EnableLocalDump: true, DumpPath: dir, DisableAniListBanners: true})
	src.Start()
	deadline := time.Now().Add(2 * time.Second)
	for !src.dump.ready() {
		if time.Now().After(deadline) {
			t.Fatal("dump never ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMangaDexCacheEviction(t *testing.T) {
	s := NewMangaDexSource(Options{})
	for i := 0; i < fetchCacheMax; i++ {
		s.cachePut(&mangaDexManga{ID: fmt.Sprintf("id-%d", i)})
	}
	s.mu.Lock()
	for k, e := range s.recent {
		e.expires = time.Now().Add(-time.Hour)
		s.recent[k] = e
		break
	}
	s.mu.Unlock()
	s.cachePut(&mangaDexManga{ID: "fresh"})
	if s.cacheGet("fresh") == nil {
		t.Fatal("fresh missing")
	}
	s.cachePut(nil)
}

func TestMoreLiveAndSourcePaths(t *testing.T) {
	b := newLiveBackendWithEndpoint("http://127.0.0.1:1")
	if _, err := b.fetch(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if d := parseRetryAfter("999"); d != 10*time.Second {
		t.Fatalf("cap = %v", d)
	}
	if d := parseRetryAfter(""); d != 2*time.Second {
		t.Fatalf("default = %v", d)
	}
	if d := parseRetryAfter("nope"); d != 2*time.Second {
		t.Fatalf("bad = %v", d)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/series/bad" {
			_, _ = w.Write([]byte(`{`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	live := newLiveBackendWithEndpoint(srv.URL)
	if _, err := live.fetch(context.Background(), "x"); err == nil {
		t.Fatal("expected status error")
	}
	if _, err := live.fetch(context.Background(), "bad"); err == nil {
		t.Fatal("expected decode error")
	}

	errBack := &errBackend{err: context.Canceled}
	src := newMangaBakaSourceWithBackends(errBack, nil, nil)
	if _, err := src.Search(context.Background(), metadata.SearchQuery{Title: "X"}); err == nil {
		t.Fatal("expected search err")
	}
	if _, err := src.Fetch(context.Background(), "1"); err == nil {
		t.Fatal("expected fetch err")
	}
	nilBack := newMangaBakaSourceWithBackends(nil, nil, nil)
	if got, err := nilBack.Search(context.Background(), metadata.SearchQuery{Title: "X"}); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := nilBack.Fetch(context.Background(), "1"); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	emptyDump := &stubBackend{isReady: true, series: nil}
	src2 := newMangaBakaSourceWithBackends(emptyDump, emptyDump, nil)
	if got, err := src2.Search(context.Background(), metadata.SearchQuery{Title: "No Match Title Here"}); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	miss, err := src2.Fetch(context.Background(), "999")
	if err != nil || miss != nil {
		t.Fatalf("%v %v", miss, err)
	}

	// banner enrichment skips bad anilist ids
	s := mbSeries(5, "Title Exact")
	s.Source = map[string]mangaBakaSourceRef{"anilist": {ID: []byte(`"nope"`)}}
	src3 := newMangaBakaSourceWithBackends(&stubBackend{isReady: true, series: []mangaBakaSeries{s}}, nil, func(context.Context, int) (string, error) {
		t.Fatal("should not call")
		return "", nil
	})
	if _, err := src3.Search(context.Background(), metadata.SearchQuery{Title: "Title Exact"}); err != nil {
		t.Fatal(err)
	}

	// cachePut eviction + flush
	src4 := newMangaBakaSourceWithBackends(&stubBackend{isReady: true}, nil, nil)
	for i := 1; i <= fetchCacheMax; i++ {
		src4.cachePut(mangaBakaSeries{ID: i, Title: "T"})
	}
	src4.mu.Lock()
	for k, e := range src4.recent {
		e.expires = time.Now().Add(-time.Hour)
		src4.recent[k] = e
		break
	}
	src4.mu.Unlock()
	src4.cachePut(mangaBakaSeries{ID: fetchCacheMax + 1, Title: "N"})
	src4.cachePut(mangaBakaSeries{ID: 0})
}

type errBackend struct{ err error }

func (e *errBackend) ready() bool { return true }
func (e *errBackend) search(context.Context, string) ([]mangaBakaSeries, error) {
	return nil, e.err
}
func (e *errBackend) fetch(context.Context, string) (*mangaBakaSeries, error) {
	return nil, e.err
}

func TestMangaDexAndAniListErrorBranches(t *testing.T) {
	// MangaDex: 500 then ok, Retry-After cap, result!=ok, empty fetch
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "99")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if strings.Contains(r.URL.Path, "/manga/") {
			_, _ = w.Write([]byte(`{"result":"ok","data":{"id":"","attributes":{"title":{},"altTitles":[],"description":{},"tags":[]},"relationships":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":"error","data":[]}`))
	}))
	defer srv.Close()
	s := NewMangaDexSource(Options{})
	s.endpoint = srv.URL
	if _, err := s.Search(context.Background(), metadata.SearchQuery{Title: "Anything Long Enough"}); err == nil {
		t.Fatal("expected result error")
	}
	n = 0
	got, err := s.Fetch(context.Background(), "missing-id")
	if err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	s2 := NewMangaDexSource(Options{})
	s2.endpoint = srv2.URL
	if _, err := s2.Search(context.Background(), metadata.SearchQuery{Title: "X"}); err == nil {
		t.Fatal("expected 500")
	}

	var an int
	asrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		an++
		if an == 1 {
			w.Header().Set("Retry-After", "99")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer asrv.Close()
	if _, err := fetchAniListByID(context.Background(), asrv.Client(), asrv.URL, 1); err == nil {
		t.Fatal("expected anilist http error")
	}

	asrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer asrv2.Close()
	if _, err := fetchAniListByID(context.Background(), asrv2.Client(), asrv2.URL, 1); err == nil {
		t.Fatal("expected 502")
	}
}

func TestDumpRefreshFailureAndStartReplace(t *testing.T) {
	prev := dumpDownloadURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	dumpDownloadURL = srv.URL
	t.Cleanup(func() { dumpDownloadURL = prev })

	dir := t.TempDir()
	b := newDumpBackend(dir, 1)
	b.refreshIfNeeded(context.Background())
	if b.ready() {
		t.Fatal("should not be ready after failed download")
	}
	_, _ = b.search(context.Background(), "x")

	b.start()
	b.start() // replaces cancel
	b.stop()
	b.stop()

	if _, ok := (*dumpIndex)(nil).builtAt(); ok {
		t.Fatal("nil index")
	}
	_ = (*dumpIndex)(nil).close()

	// skip id=0 during ingest
	path := filepath.Join(dir, "series.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":0,"title":"Zero","type":"manga"}`+"\n"+`{"id":8,"title":"Eight","type":"manga"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := buildDumpIndex(context.Background(), path, filepath.Join(dir, "i2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.close()
	if got, _ := idx.fetchByID(context.Background(), "0"); got != nil {
		t.Fatal(got)
	}
	if got, _ := idx.fetchByID(context.Background(), "8"); got == nil {
		t.Fatal("missing 8")
	}

	// people() nil attributes / empty names
	m := mangaDexManga{}
	m.Relationships = []struct {
		Type       string `json:"type"`
		Attributes *struct {
			Name     string `json:"name"`
			FileName string `json:"fileName"`
		} `json:"attributes"`
	}{
		{Type: "author", Attributes: nil},
		{Type: "author", Attributes: &struct {
			Name     string `json:"name"`
			FileName string `json:"fileName"`
		}{Name: ""}},
	}
	_ = m.people()
}

func TestResolveDumpDirTempFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	got, err := resolveDumpDir("")
	if err != nil || got == "" {
		t.Fatal(got, err)
	}
}

func TestTinyCoverageGaps(t *testing.T) {
	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "series.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":1,"title":"Y","type":"manga"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "index.sqlite")
	idx, err := buildDumpIndex(context.Background(), jsonlPath, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.close()
	if got, err := idx.lookup(context.Background(), "   "); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	// corrupt stored json for fetchByID unmarshal error
	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(`UPDATE series SET json = '{bad' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	_ = rw.Close()
	idx2, err := openDumpIndex(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.close()
	if _, err := idx2.fetchByID(context.Background(), "1"); err == nil {
		t.Fatal("expected unmarshal error")
	}

	// full flush when all cache entries hot
	src := newMangaBakaSourceWithBackends(&stubBackend{isReady: true}, nil, nil)
	for i := 1; i <= fetchCacheMax; i++ {
		src.cachePut(mangaBakaSeries{ID: i})
	}
	src.cachePut(mangaBakaSeries{ID: fetchCacheMax + 2})

	// enrichBanner: no anilist key
	s := mbSeries(9, "Exact Title Nine")
	src2 := newMangaBakaSourceWithBackends(&stubBackend{isReady: true, series: []mangaBakaSeries{s}}, nil, func(context.Context, int) (string, error) {
		t.Fatal("no")
		return "", nil
	})
	if _, err := src2.Search(context.Background(), metadata.SearchQuery{Title: "Exact Title Nine"}); err != nil {
		t.Fatal(err)
	}
}
