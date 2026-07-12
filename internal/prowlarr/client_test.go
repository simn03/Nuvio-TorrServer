package prowlarr

import (
	"encoding/hex"
	"strings"
	"testing"
)

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
