package prowlarr

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type noopCache struct{}

func (noopCache) ProwlarrCacheGet(string) ([]byte, bool)               { return nil, false }
func (noopCache) ProwlarrCacheSet(string, []byte, time.Duration) error { return nil }

func TestSearchSendsIndexerIds(t *testing.T) {
	var gotIndexers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIndexers = r.URL.Query()["indexerIds"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"X","indexer":"TheRARBG","indexerId":6}]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", noopCache{}, time.Hour, false)
	res, err := c.Search(context.Background(), "q", []int{2000}, []int{6, 3})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(gotIndexers) != 2 || gotIndexers[0] != "6" || gotIndexers[1] != "3" {
		t.Errorf("indexerIds sent = %v, want [6 3]", gotIndexers)
	}
	if len(res) != 1 || res[0].Indexer != "TheRARBG" || res[0].IndexerID != 6 {
		t.Errorf("result indexer not decoded: %+v", res)
	}
}

func TestCacheKeyVariesByIndexers(t *testing.T) {
	base := cacheKey("q", []int{2000}, nil)
	withIdx := cacheKey("q", []int{2000}, []int{6})
	if base == withIdx {
		t.Error("cache key should differ when indexer selection differs")
	}
	// Order-independent.
	if cacheKey("q", []int{2000}, []int{6, 3}) != cacheKey("q", []int{2000}, []int{3, 6}) {
		t.Error("cache key should be order-independent for indexer ids")
	}
}

func TestNormalizeInfoHash(t *testing.T) {
	valid40 := "224bf45881aaaaaaaaaaaaaaaaaaaaaaaaaa1c68"
	// Double-hex-encoded form: hex(ascii(valid40)).
	doubled := hex.EncodeToString([]byte(valid40))
	if len(doubled) != 80 {
		t.Fatalf("test setup: doubled len = %d", len(doubled))
	}

	cases := []struct{ in, want string }{
		{valid40, valid40},
		{strings.ToUpper(valid40), valid40}, // lowercased
		{doubled, valid40},                  // unwrapped
		{"MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U", "MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U"}, // base32 (32 chars)
		{"", ""},
		{"nothex-nothex-nothex-nothex-nothexxx", ""},
		{"tooShort", ""},
	}
	for _, c := range cases {
		if got := NormalizeInfoHash(c.in); got != c.want {
			t.Errorf("NormalizeInfoHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLinkPriority(t *testing.T) {
	// magnetUrl wins.
	r := Result{MagnetURL: "magnet:?xt=urn:btih:abc", InfoHash: "224bf45881aaaaaaaaaaaaaaaaaaaaaaaaaa1c68", DownloadURL: "http://x"}
	if r.Link() != "magnet:?xt=urn:btih:abc" {
		t.Errorf("magnetUrl should win, got %q", r.Link())
	}

	// No magnetUrl: synth from a (double-encoded) infohash.
	doubled := hex.EncodeToString([]byte("224bf45881aaaaaaaaaaaaaaaaaaaaaaaaaa1c68"))
	r = Result{InfoHash: doubled, DownloadURL: "http://x"}
	if got := r.Link(); got != "magnet:?xt=urn:btih:224bf45881aaaaaaaaaaaaaaaaaaaaaaaaaa1c68" {
		t.Errorf("infohash synth wrong: %q", got)
	}

	// Only downloadUrl.
	r = Result{DownloadURL: "http://x/torrent"}
	if r.Link() != "http://x/torrent" {
		t.Errorf("downloadUrl fallback wrong: %q", r.Link())
	}

	// Nothing usable.
	if (Result{}).Link() != "" {
		t.Errorf("empty result should have no link")
	}
}
