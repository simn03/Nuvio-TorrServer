package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type wrappedLogLine struct {
	Time   string `json:"time"`
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Source string `json:"source"`
}

func runLogwrap(args []string) error {
	fs := flag.NewFlagSet("logwrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "process", "source process name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return wrapLogs(os.Stdin, os.Stdout, *source, time.Now)
}

func wrapLogs(r io.Reader, w io.Writer, source string, now func() time.Time) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			continue
		}
		if err := enc.Encode(formatWrappedLogLine(source, line, now)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	return nil
}

func writeLogwrapError(w io.Writer, source string, err error) {
	_ = json.NewEncoder(w).Encode(wrappedLogLine{
		Time:   time.Now().Format(time.RFC3339Nano),
		Level:  "ERROR",
		Msg:    err.Error(),
		Source: source,
	})
}

func formatWrappedLogLine(source, line string, now func() time.Time) wrappedLogLine {
	t := now()
	msg := line
	if parsed, rest, ok := parseTorrServerTimestamp(line); ok {
		t = parsed
		msg = rest
	}
	return wrappedLogLine{
		Time:   t.Format(time.RFC3339Nano),
		Level:  "INFO",
		Msg:    strings.TrimSpace(msg),
		Source: source,
	}
}

func parseTorrServerTimestamp(line string) (time.Time, string, bool) {
	const layout = "2006/01/02 15:04:05"
	if len(line) <= len(layout) || line[len(layout)] != ' ' {
		return time.Time{}, "", false
	}
	ts, err := time.ParseInLocation(layout, line[:len(layout)], time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, line[len(layout)+1:], true
}
