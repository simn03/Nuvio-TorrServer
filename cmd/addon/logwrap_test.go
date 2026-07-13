package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWrapLogsFormatsTorrServerLineAsJSON(t *testing.T) {
	in := strings.NewReader("2026/07/13 00:41:07 Torrent close by timeout cc201b38d4504163bd811f0c2b3a528acb77f50a\n")
	var out bytes.Buffer

	if err := wrapLogs(in, &out, "torrserver", func() time.Time {
		return time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	}); err != nil {
		t.Fatalf("wrapLogs: %v", err)
	}

	var got wrappedLogLine
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not json: %v; %q", err, out.String())
	}
	if got.Level != "INFO" || got.Source != "torrserver" {
		t.Fatalf("unexpected fields: %+v", got)
	}
	if got.Msg != "Torrent close by timeout cc201b38d4504163bd811f0c2b3a528acb77f50a" {
		t.Fatalf("msg = %q", got.Msg)
	}
	if !strings.HasPrefix(got.Time, "2026-07-13T00:41:07") {
		t.Fatalf("time = %q", got.Time)
	}
}

func TestWrapLogsUsesCurrentTimeWhenNoTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 2, 3, 4, time.UTC)
	got := formatWrappedLogLine("torrserver", "plain line", func() time.Time { return now })

	if got.Time != "2026-07-13T01:02:03.000000004Z" {
		t.Fatalf("time = %q", got.Time)
	}
	if got.Msg != "plain line" {
		t.Fatalf("msg = %q", got.Msg)
	}
}
