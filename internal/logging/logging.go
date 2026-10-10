// Package logging provides the log handlers of the server, in text and JSON,
// and the representation of untrusted values in logs.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"
)

// timeFormat is RFC 3339 in UTC with milliseconds, used by both formats.
const timeFormat = "2006-01-02T15:04:05.000Z"

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText, FormatJSON:
		return Format(s), nil
	default:
		return "", fmt.Errorf("unknown log format %q, want %q or %q", s, FormatText, FormatJSON)
	}
}

func NewHandler(w io.Writer, format Format, level slog.Leveler) slog.Handler {
	if format == FormatJSON {
		return slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level:       level,
			ReplaceAttr: replaceJSONAttr,
		})
	}
	return NewTextHandler(w, level)
}

// replaceJSONAttr writes times like the text format, and durations as numbers
// of milliseconds with the unit in the key ("duration_ms": 10.2) rather than
// in nanoseconds.
func replaceJSONAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindTime:
		return slog.String(a.Key, a.Value.Time().UTC().Format(timeFormat))
	case slog.KindDuration:
		return slog.Float64(a.Key+"_ms", float64(a.Value.Duration())/float64(time.Millisecond))
	}
	return a
}

// Untrusted returns an attribute for a value sent by a client. Its text is
// escaped like a Go string, so that it cannot be mistaken for other log
// content, and the text handler always quotes it.
func Untrusted(key, value string) slog.Attr {
	return slog.Any(key, untrusted(value))
}

// Error returns the attribute of err, logged like untrusted values: errors can
// hold text that clients or servers sent.
func Error(err error) slog.Attr {
	return Untrusted("error", err.Error())
}

type untrusted string

func (u untrusted) MarshalText() ([]byte, error) {
	quoted := strconv.Quote(string(u))
	return []byte(quoted[1 : len(quoted)-1]), nil
}

func (u untrusted) MarshalJSON() ([]byte, error) {
	text, _ := u.MarshalText()
	return json.Marshal(string(text))
}
