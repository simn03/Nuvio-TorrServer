package resolver

import (
	"testing"

	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

func TestSelectFileSeriesEpisodeMatch(t *testing.T) {
	files := []torrserver.File{
		{ID: 1, Path: "Show S01/Show.S01E01.1080p.mkv", Length: 1_000_000_000},
		{ID: 2, Path: "Show S01/Show.S01E02.1080p.mkv", Length: 1_000_000_000},
		{ID: 3, Path: "Show S01/Show.S01E03.1080p.mkv", Length: 1_000_000_000},
		{ID: 4, Path: "Show S01/poster.jpg", Length: 50_000},
	}
	got := selectFile(files, MediaID{Series: true, Season: 1, Episode: 2})
	if got != 2 {
		t.Errorf("season-pack selection = %d, want 2", got)
	}
}

func TestSelectFileMovieLargestVideo(t *testing.T) {
	files := []torrserver.File{
		{ID: 1, Path: "Movie/sample.mkv", Length: 50_000_000},
		{ID: 2, Path: "Movie/Movie.2020.1080p.mkv", Length: 8_000_000_000},
		{ID: 3, Path: "Movie/subs.srt", Length: 40_000},
	}
	got := selectFile(files, MediaID{})
	if got != 2 {
		t.Errorf("movie selection = %d, want 2 (largest video)", got)
	}
}

func TestSelectFileNoVideoFallsBackToFirst(t *testing.T) {
	files := []torrserver.File{{ID: 7, Path: "readme.txt", Length: 10}}
	if got := selectFile(files, MediaID{}); got != 7 {
		t.Errorf("fallback = %d, want first file id 7", got)
	}
}

func TestSelectFileEmptyFallsBackToOne(t *testing.T) {
	if got := selectFile(nil, MediaID{}); got != 1 {
		t.Errorf("empty file list = %d, want 1", got)
	}
}

func TestSelectFileSeriesNoEpisodeMatchUsesLargest(t *testing.T) {
	// Requested E5 not present; should fall back to the largest video file.
	files := []torrserver.File{
		{ID: 1, Path: "Show.S01E01.mkv", Length: 1_000_000_000},
		{ID: 2, Path: "Show.S01E02.mkv", Length: 3_000_000_000},
	}
	if got := selectFile(files, MediaID{Series: true, Season: 1, Episode: 5}); got != 2 {
		t.Errorf("no-match fallback = %d, want 2 (largest)", got)
	}
}
