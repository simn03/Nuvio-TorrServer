package config

import (
	"reflect"
	"testing"
)

func TestDefaultRoundTrip(t *testing.T) {
	d := Default()
	blob, err := Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Unmarshal(blob)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, d)
	}
}

func TestNormalizeClampsAndFilters(t *testing.T) {
	in := UserConfig{
		Sort:             "bogus",              // -> quality
		Resolutions:      []string{"1080p", "8k", "720p"}, // 8k dropped, order canonicalised
		ExcludeQualities: []string{"cam", "nonsense"},     // nonsense dropped
		PreferHEVC:       false,
		MaxSizeGB:        -5,  // -> 0
		MinSeeders:       -1,  // -> 0
		MaxResults:       999, // -> 50
	}
	got := Normalize(in)

	if got.Sort != "quality" {
		t.Errorf("Sort = %q, want quality", got.Sort)
	}
	if !reflect.DeepEqual(got.Resolutions, []string{"1080p", "720p"}) {
		t.Errorf("Resolutions = %v, want [1080p 720p]", got.Resolutions)
	}
	if !reflect.DeepEqual(got.ExcludeQualities, []string{"cam"}) {
		t.Errorf("ExcludeQualities = %v, want [cam]", got.ExcludeQualities)
	}
	if got.MaxSizeGB != 0 {
		t.Errorf("MaxSizeGB = %v, want 0", got.MaxSizeGB)
	}
	if got.MinSeeders != 0 {
		t.Errorf("MinSeeders = %v, want 0", got.MinSeeders)
	}
	if got.MaxResults != 50 {
		t.Errorf("MaxResults = %v, want 50", got.MaxResults)
	}
}

func TestNormalizeEmptyResolutionsMeansAll(t *testing.T) {
	got := Normalize(UserConfig{Sort: "size", MaxResults: 5})
	if !reflect.DeepEqual(got.Resolutions, AllResolutions) {
		t.Errorf("empty Resolutions = %v, want all %v", got.Resolutions, AllResolutions)
	}
}
