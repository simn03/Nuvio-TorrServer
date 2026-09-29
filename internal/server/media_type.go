package server

import (
	"mime"
	"path/filepath"
	"strings"
)

// Keep video types available in minimal images without a system MIME database.
// The AVI, MOV and TS aliases match TorrServer's streaming MIME mappings.
var videoMediaTypes = map[string]string{
	".mkv":  "video/x-matroska",
	".mp4":  "video/mp4",
	".avi":  "video/avi",
	".m4v":  "video/mp4",
	".mov":  "video/x-quicktime",
	".ts":   "video/mpeg",
	".wmv":  "video/x-ms-wmv",
	".flv":  "video/x-flv",
	".webm": "video/webm",
	".mpg":  "video/mpeg",
	".mpeg": "video/mpeg",
}

func mediaType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if typ, ok := videoMediaTypes[ext]; ok {
		return typ
	}
	if typ := mime.TypeByExtension(ext); typ != "" {
		return typ
	}
	return "application/octet-stream"
}
