package logging

import (
	"context"
	"encoding"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strconv"
	"sync"
	"unicode"
	"unicode/utf8"
)

// RemoteKey is the key of the attribute holding the address of the client,
// which the text handler writes before the message.
const RemoteKey = "remote"

// TextHandler writes records as lines such as:
//
//	[2026-10-10T23:22:16.155Z] INFO  [192.0.2.1]:51006 server list ping: protocol=777 server_address="mc.example.com"
//
// The time is in UTC, the remote address is written before the message, and
// the other attributes after it. Values are quoted and escaped like Go
// strings if they contain spaces, '=', '"', '\' or non-printable characters;
// untrusted values always are.
type TextHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	level slog.Leveler
	attrs []byte // Preformatted attributes, each preceded by a space.
	group string // Prefix of the keys, such as "a.b.".
}

func NewTextHandler(w io.Writer, level slog.Leveler) *TextHandler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &TextHandler{w: w, mu: &sync.Mutex{}, level: level}
}

func (h *TextHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *TextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := *h
	// Without spare capacity, appending copies the attributes, so that handlers
	// derived from the same one do not overwrite each other's.
	h2.attrs = h.attrs[:len(h.attrs):len(h.attrs)]
	for _, a := range attrs {
		h2.attrs = appendAttr(h2.attrs, h.group, a)
	}
	return &h2
}

func (h *TextHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.group = h.group + name + "."
	return &h2
}

// lineBuffers holds the buffers that lines are written in, so that each line
// does not allocate its own.
var lineBuffers = sync.Pool{New: func() any { return new([]byte) }}

// maxPooledLine bounds the buffers kept in lineBuffers, so that a long line,
// such as one with a long error, does not keep its memory.
const maxPooledLine = 4 << 10

func (h *TextHandler) Handle(_ context.Context, r slog.Record) error {
	pooled := lineBuffers.Get().(*[]byte)
	buf := (*pooled)[:0]
	if !r.Time.IsZero() {
		buf = append(buf, '[')
		buf = r.Time.UTC().AppendFormat(buf, timeFormat)
		buf = append(buf, "] "...)
	}
	buf = appendLevel(buf, r.Level)

	r.Attrs(func(a slog.Attr) bool {
		if h.isRemote(a) {
			buf = appendRemote(buf, a.Value)
			buf = append(buf, ' ')
		}
		return true
	})

	buf = appendMessage(buf, r.Message)
	colon := len(buf)
	buf = append(buf, ':')
	buf = append(buf, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		if !h.isRemote(a) {
			buf = appendAttr(buf, h.group, a)
		}
		return true
	})
	if len(buf) == colon+1 {
		buf = buf[:colon] // No attributes, so no colon either.
	}
	buf = append(buf, '\n')

	h.mu.Lock()
	_, err := h.w.Write(buf)
	h.mu.Unlock()
	if cap(buf) <= maxPooledLine {
		*pooled = buf
		lineBuffers.Put(pooled)
	}
	return err
}

// isRemote reports whether a holds the address of the client, which is written
// before the message. In a group, it is an ordinary attribute.
func (h *TextHandler) isRemote(a slog.Attr) bool {
	return h.group == "" && a.Key == RemoteKey
}

// levelWidth is the width to which levels are padded, like log4j does: the
// length of the longest level names ("DEBUG" and "ERROR").
const levelWidth = 5

func appendLevel(buf []byte, level slog.Level) []byte {
	s := level.String()
	buf = append(buf, s...)
	for i := len(s); i < levelWidth; i++ {
		buf = append(buf, ' ')
	}
	return append(buf, ' ')
}

// appendRemote appends an address as [ip]:port, for IPv4 and IPv6 alike.
func appendRemote(buf []byte, v slog.Value) []byte {
	if addr, ok := v.Resolve().Any().(netip.AddrPort); ok && addr.IsValid() {
		buf = append(buf, '[')
		buf = addr.Addr().AppendTo(buf)
		buf = append(buf, "]:"...)
		return strconv.AppendUint(buf, uint64(addr.Port()), 10)
	}
	return appendValue(buf, v)
}

func appendMessage(buf []byte, msg string) []byte {
	for _, r := range msg {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return strconv.AppendQuote(buf, msg)
		}
	}
	return append(buf, msg...)
}

func appendAttr(buf []byte, group string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return buf
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			group += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			buf = appendAttr(buf, group, ga)
		}
		return buf
	}
	buf = append(buf, ' ')
	buf = appendString(buf, group+a.Key)
	buf = append(buf, '=')
	return appendValue(buf, a.Value)
}

func appendValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return appendString(buf, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(buf, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(buf, v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.AppendFloat(buf, v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.AppendBool(buf, v.Bool())
	case slog.KindTime:
		return v.Time().UTC().AppendFormat(buf, timeFormat)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case untrusted:
			return strconv.AppendQuote(buf, string(x))
		case error:
			return appendString(buf, x.Error())
		case encoding.TextMarshaler:
			text, err := x.MarshalText()
			if err != nil {
				return appendString(buf, "!ERROR:"+err.Error())
			}
			return appendString(buf, string(text))
		default:
			return appendString(buf, fmt.Sprint(x))
		}
	default:
		// Durations.
		return appendString(buf, v.String())
	}
}

func appendString(buf []byte, s string) []byte {
	if needsQuoting(s) {
		return strconv.AppendQuote(buf, s)
	}
	return append(buf, s...)
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r < utf8.RuneSelf {
			// Most values are ASCII, checked without unicode.IsPrint.
			if r <= ' ' || r == '=' || r == '"' || r == '\\' || r == 0x7f {
				return true
			}
		} else if r == utf8.RuneError || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}
