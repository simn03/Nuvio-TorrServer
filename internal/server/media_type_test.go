package server

import "testing"

func TestMediaType(t *testing.T) {
	for _, tt := range []struct {
		path string
		want string
	}{
		{"film.mkv", "video/x-matroska"},
		{"film.mp4", "video/mp4"},
		{"film.avi", "video/avi"},
		{"film.m4v", "video/mp4"},
		{"film.mov", "video/x-quicktime"},
		{"film.ts", "video/mpeg"},
		{"film.wmv", "video/x-ms-wmv"},
		{"film.flv", "video/x-flv"},
		{"film.webm", "video/webm"},
		{"film.mpg", "video/mpeg"},
		{"film.mpeg", "video/mpeg"},
		{"Show/Show.S01E01.MKV", "video/x-matroska"},
		{"Show/Show.S01E01.Mp4", "video/mp4"},
		{"poster.jpg", "image/jpeg"},
		{"film.unknown-video-format", "application/octet-stream"},
		{"film", "application/octet-stream"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			if got := mediaType(tt.path); got != tt.want {
				t.Errorf("mediaType(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
