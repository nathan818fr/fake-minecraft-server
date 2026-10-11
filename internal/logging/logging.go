// Package logging provides the log handlers of the server, in text and JSON,
// and the representation of untrusted values in logs.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// timeFormat is RFC 3339 in UTC with milliseconds, used by both formats.
const timeFormat = "2006-01-02T15:04:05.000Z"

// durationPrecision is the precision of logged durations: finer digits would
// only be noise.
const durationPrecision = 100 * time.Microsecond

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseFormat parses the name of a log format, in any case.
func ParseFormat(s string) (Format, error) {
	switch format := Format(strings.ToLower(s)); format {
	case FormatText, FormatJSON:
		return format, nil
	}
	return "", fmt.Errorf("must be %s or %s, got %q", FormatText, FormatJSON, s)
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

// Duration returns an attribute for d, rounded to durationPrecision.
func Duration(key string, d time.Duration) slog.Attr {
	return slog.Duration(key, d.Round(durationPrecision))
}

// MaxUntrustedLength bounds the length, in bytes, of the untrusted values in
// logs that could otherwise be as long as clients or servers want.
const MaxUntrustedLength = 64

// Shorten returns s cut to MaxUntrustedLength bytes, followed by "..." if it is
// cut. The cut never splits a character.
func Shorten(s string) string {
	if len(s) <= MaxUntrustedLength {
		return s
	}
	n := MaxUntrustedLength
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// Untrusted returns an attribute for a value sent by a client or a server. Its
// text is escaped like a Go string, so that it cannot be mistaken for other log
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
