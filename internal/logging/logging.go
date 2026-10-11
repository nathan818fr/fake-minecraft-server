// Package logging provides the log handlers of the server, in text and JSON,
// and the representation of untrusted values in logs.
package logging

import (
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"
	"unicode/utf16"
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

func ParseFormat(s string) (Format, error) {
	switch format := Format(s); format {
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

// replaceJSONAttr writes times like the text format, durations as numbers of
// milliseconds with the unit in the key ("duration_ms": 10.2) rather than in
// nanoseconds, and the values that have a text form, such as addresses, and
// the untrusted values that need no escape, as strings, which the handler
// writes itself: it encodes other values with encoding/json, which takes
// almost half of a line.
func replaceJSONAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindTime:
		return slog.String(a.Key, a.Value.Time().UTC().Format(timeFormat))
	case slog.KindDuration:
		return slog.Float64(a.Key+"_ms", float64(a.Value.Duration())/float64(time.Millisecond))
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case untrusted:
			if printable(string(v)) {
				return slog.String(a.Key, string(v))
			}
			return slog.Any(a.Key, jsonString(v))
		case encoding.TextMarshaler:
			if text, err := v.MarshalText(); err == nil {
				return slog.String(a.Key, string(text))
			}
		}
	}
	return a
}

// Duration returns an attribute for d, rounded to durationPrecision.
func Duration(key string, d time.Duration) slog.Attr {
	return slog.Duration(key, d.Round(durationPrecision))
}

// MaxUntrustedLength bounds the length, in bytes, of the untrusted values in
// logs that nothing else bounds, such as the version names of servers, which
// can be as long as their status (see Shorten). Values that the protocol
// bounds, such as hosts, are logged whole.
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

// Untrusted returns an attribute for a value sent by a client or a server,
// escaped in logs so that it cannot forge their content.
func Untrusted(key, value string) slog.Attr {
	return slog.Any(key, untrusted(value))
}

// Error returns the attribute of err, logged like untrusted values: errors can
// hold text that clients or servers sent.
func Error(err error) slog.Attr {
	return Untrusted("error", err.Error())
}

type untrusted string

// printable reports whether s is valid UTF-8 made of printable characters
// only, which the JSON handler writes as they are.
func printable(s string) bool {
	for _, r := range s {
		if r == utf8.RuneError || !strconv.IsPrint(r) {
			return false
		}
	}
	return true
}

// jsonString is an untrusted value written as a JSON string with its
// non-printable characters escaped, which the JSON handler leaves raw, such as
// bidirectional ones. Invalid UTF-8 becomes U+FFFD.
type jsonString string

func (s jsonString) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, len(s)+2)
	buf = append(buf, '"')
	for _, r := range string(s) {
		switch {
		case r == '"' || r == '\\':
			buf = append(buf, '\\', byte(r))
		case r == '\n':
			buf = append(buf, `\n`...)
		case r == '\r':
			buf = append(buf, `\r`...)
		case r == '\t':
			buf = append(buf, `\t`...)
		case r != utf8.RuneError && strconv.IsPrint(r):
			buf = utf8.AppendRune(buf, r)
		case r > 0xffff:
			r1, r2 := utf16.EncodeRune(r)
			buf = appendUnicodeEscape(appendUnicodeEscape(buf, r1), r2)
		default:
			buf = appendUnicodeEscape(buf, r)
		}
	}
	return append(buf, '"'), nil
}

// appendUnicodeEscape appends r, at most 0xffff, as a JSON \u escape.
func appendUnicodeEscape(buf []byte, r rune) []byte {
	const hex = "0123456789abcdef"
	return append(buf, '\\', 'u', hex[r>>12&0xf], hex[r>>8&0xf], hex[r>>4&0xf], hex[r&0xf])
}
