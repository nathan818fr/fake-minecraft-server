package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/slogtest"
	"time"
	"unicode"
)

var testTime = time.Date(2026, 10, 10, 23, 22, 16, 155_123_456, time.FixedZone("CEST", 2*60*60))

func logLine(h slog.Handler, level slog.Level, msg string, attrs ...slog.Attr) {
	r := slog.NewRecord(testTime, level, msg, 0)
	r.AddAttrs(attrs...)
	_ = h.Handle(context.Background(), r)
}

func TestTextHandler(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		msg   string
		attrs []slog.Attr
		want  string
	}{
		{
			name:  "connection",
			level: slog.LevelInfo,
			msg:   "server list ping",
			attrs: []slog.Attr{
				slog.Any("remote", netip.MustParseAddrPort("192.0.2.1:51006")),
				slog.Int("protocol", 777),
				Untrusted("server_address", "mc.example.com"),
				slog.Bool("ping", true),
				slog.Duration("duration", 10200*time.Microsecond),
			},
			want: `[2026-10-10T21:22:16.155Z] INFO  [192.0.2.1]:51006 server list ping: protocol=777 server_address="mc.example.com" ping=true duration=10.2ms`,
		},
		{
			name:  "IPv6 remote",
			level: slog.LevelWarn,
			msg:   "rejected",
			attrs: []slog.Attr{slog.Any("remote", netip.MustParseAddrPort("[2001:db8::1]:25565"))},
			want:  `[2026-10-10T21:22:16.155Z] WARN  [2001:db8::1]:25565 rejected`,
		},
		{
			name:  "no attributes",
			level: slog.LevelInfo,
			msg:   "stopped",
			want:  `[2026-10-10T21:22:16.155Z] INFO  stopped`,
		},
		{
			name:  "levels",
			level: slog.LevelError,
			msg:   "failed",
			attrs: []slog.Attr{slog.Any("error", errors.New("boom"))},
			want:  `[2026-10-10T21:22:16.155Z] ERROR failed: error=boom`,
		},
		{
			name:  "quoting",
			level: slog.LevelInfo,
			msg:   "values",
			attrs: []slog.Attr{
				slog.String("plain", "abc-é"),
				slog.String("space", "a b"),
				slog.String("equal", "a=b"),
				slog.String("quote", `a"b`),
				slog.String("backslash", `a\nb`),
				slog.String("empty", ""),
				slog.String("bidi", "a\u202eb"),
				Untrusted("untrusted", "abc"),
				slog.Any("addr", netip.MustParseAddr("192.0.2.1")),
				slog.Bool("bool", true),
				{},
			},
			want: `[2026-10-10T21:22:16.155Z] INFO  values: plain=abc-é space="a b" equal="a=b" quote="a\"b" backslash="a\\nb" empty="" bidi="a\u202eb" untrusted="abc" addr=192.0.2.1 bool=true`,
		},
		{
			name:  "groups",
			level: slog.LevelInfo,
			msg:   "grouped",
			attrs: []slog.Attr{slog.Group("g", slog.Int("a", 1), slog.Group("h", slog.Int("b", 2))), slog.Group("", slog.Int("c", 3))},
			want:  `[2026-10-10T21:22:16.155Z] INFO  grouped: g.a=1 g.h.b=2 c=3`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logLine(NewTextHandler(&buf, nil), tc.level, tc.msg, tc.attrs...)
			if got := buf.String(); got != tc.want+"\n" {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestTextHandlerConformance runs the standard tests of slog handlers, which
// read the lines back as maps.
func TestTextHandlerConformance(t *testing.T) {
	var buf bytes.Buffer
	err := slogtest.TestHandler(NewTextHandler(&buf, nil), func() []map[string]any {
		var records []map[string]any
		for _, line := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
			records = append(records, parseTextLine(t, line))
		}
		return records
	})
	if err != nil {
		t.Error(err)
	}
}

var textLineRE = regexp.MustCompile(`^(?:\[(\S+)\] )?(\S+) +(.*?)(?:: (.*))?$`)

// parseTextLine parses a line without remote address, nesting grouped keys.
func parseTextLine(t *testing.T, line string) map[string]any {
	t.Helper()
	m := textLineRE.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("malformed line %q", line)
	}
	record := map[string]any{slog.LevelKey: m[2], slog.MessageKey: m[3]}
	if m[1] != "" {
		record[slog.TimeKey] = m[1]
	}
	for attrs := m[4]; attrs != ""; {
		key, rest, _ := strings.Cut(attrs, "=")
		var value string
		if quoted, err := strconv.QuotedPrefix(rest); err == nil {
			value, _ = strconv.Unquote(quoted)
			rest = rest[len(quoted):]
		} else {
			value, rest, _ = strings.Cut(rest, " ")
			rest = " " + rest
		}
		attrs = strings.TrimPrefix(rest, " ")

		group := record
		keys := strings.Split(key, ".")
		for _, k := range keys[:len(keys)-1] {
			if _, ok := group[k].(map[string]any); !ok {
				group[k] = map[string]any{}
			}
			group = group[k].(map[string]any)
		}
		group[keys[len(keys)-1]] = value
	}
	return record
}

func TestJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	logLine(NewHandler(&buf, FormatJSON, nil), slog.LevelInfo, "server list ping",
		slog.Any("remote", netip.MustParseAddrPort("192.0.2.1:51006")),
		Untrusted("server_address", "mc.example.com"),
		Untrusted("data", "\x16\x03\x01\nGET"),
		Untrusted("version", `§c"Maintenance"`),
		slog.Duration("duration", 10200*time.Microsecond),
	)
	want := `{"time":"2026-10-10T21:22:16.155Z","level":"INFO","msg":"server list ping","remote":"192.0.2.1:51006","server_address":"mc.example.com","data":"\u0016\u0003\u0001\nGET","version":"§c\"Maintenance\"","duration_ms":10.2}` + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got  %swant %s", got, want)
	}
}

// Untrusted values must not be able to forge lines or attributes, nor to hide
// or disguise content with control or invisible characters, in any format.
func TestUntrustedValuesCannotInjectContent(t *testing.T) {
	malicious := []string{
		"evil\nlevel=ERROR msg=\"hacked\"",
		"evil\r\n[2026-10-10T21:22:16.155Z] ERROR hacked",
		`evil\nliteral`,
		`evil" result=pong`,
		"evil\x1b[31mred\x1b[0m",
		"evil\u202eright-to-left",
		"evil\u2028\u0085\u200b",
		"evil\xff\xfe",
		"evil\x00",
		"",
	}
	for _, format := range []Format{FormatText, FormatJSON} {
		for _, value := range malicious {
			var buf bytes.Buffer
			logLine(NewHandler(&buf, format, nil), slog.LevelInfo, "server list ping", Untrusted("address", value), slog.Int("port", 1))
			out := buf.String()

			if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
				t.Errorf("%s, %q: not a single line: %q", format, value, out)
			}
			for _, r := range strings.TrimSuffix(out, "\n") {
				if r == '�' || !unicode.IsPrint(r) {
					t.Errorf("%s, %q: raw character %U in %q", format, value, r, out)
				}
			}

			var address string
			want := value
			switch format {
			case FormatText:
				_, attrs, _ := strings.Cut(out, ": ")
				quoted, rest, ok := strings.Cut(strings.TrimPrefix(attrs, "address="), " port=1\n")
				if !ok || rest != "" {
					t.Errorf("text, %q: unexpected attributes %q", value, attrs)
					continue
				}
				var err error
				if address, err = strconv.Unquote(quoted); err != nil {
					t.Errorf("text, %q: %v", value, err)
				}
			case FormatJSON:
				var record map[string]any
				if err := json.Unmarshal([]byte(out), &record); err != nil {
					t.Errorf("json, %q: %v", value, err)
					continue
				}
				if len(record) != 5 {
					t.Errorf("json, %q: unexpected fields in %q", value, out)
				}
				address = record["address"].(string)
				want = string([]rune(value)) // Invalid UTF-8 becomes U+FFFD, one per byte.
			}
			if address != want {
				t.Errorf("%s, %q: address decodes to %q, want %q", format, value, address, want)
			}
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"text", "json"} {
		if f, err := ParseFormat(s); err != nil || string(f) != s {
			t.Errorf("ParseFormat(%q) = %q, %v", s, f, err)
		}
	}
	for _, s := range []string{"", "JSON", "logfmt"} {
		if _, err := ParseFormat(s); err == nil {
			t.Errorf("ParseFormat(%q) succeeded, want an error", s)
		}
	}
}

// Errors are escaped like untrusted values, which they can hold.
func TestError(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewHandler(&buf, FormatJSON, nil)).Info("failed", Error(errors.New("bad \u202e\n")))
	if want := `"error":"bad \u202e\n"`; !strings.Contains(buf.String(), want) {
		t.Errorf("log = %q, want it to contain %q", buf.String(), want)
	}
}
