// Package legacyproto implements the part of the protocol of clients older than
// 1.7 needed to answer their server list pings and kick them when they try to
// join. Unlike in the modern protocol, its packets have no length prefix.
//
// Its protocol versions have their own numbering, which restarted with the
// modern protocol: 47 is 1.4.2 here, but 1.8 in the modern protocol.
//
// See https://minecraft.wiki/w/Java_Edition_protocol/Server_List_Ping
package legacyproto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
)

const kickID = 0xff

// maxKickLength is the longest string, in UTF-16 code units, that legacy
// clients accept in kick packets, which also carry the responses to pings.
const maxKickLength = 256

// maxServerAddressLength matches the modern limit.
const maxServerAddressLength = 255

// maxUsernameLength is the vanilla limit. As valid usernames are ASCII, it is
// also applied to their length in bytes, like in the modern protocol.
const maxUsernameLength = 16

// unknownProtocolVersion is used for clients that do not send their protocol
// version: in logins before 1.3 and pings before 1.6.
const unknownProtocolVersion = -1

// AppendKick appends a kick packet holding s, which disconnects legacy
// clients with s as message, cut to their length limit. Invalid UTF-8 is sent
// as U+FFFD.
func AppendKick(b []byte, s string) []byte {
	s = shortenUTF16(s, maxKickLength)
	b = append(b, kickID)
	b = binary.BigEndian.AppendUint16(b, uint16(utf16Length(s)))
	for _, r := range s {
		if utf16.RuneLen(r) == 2 {
			high, low := utf16.EncodeRune(r)
			b = binary.BigEndian.AppendUint16(b, uint16(high))
			b = binary.BigEndian.AppendUint16(b, uint16(low))
		} else {
			b = binary.BigEndian.AppendUint16(b, uint16(r))
		}
	}
	return b
}

// utf16Length returns the length of s in UTF-16 code units, with invalid UTF-8
// as U+FFFD.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// shortenUTF16 returns s cut to maxLength UTF-16 code units, without splitting
// a surrogate pair.
func shortenUTF16(s string, maxLength int) string {
	n := 0
	for i, r := range s {
		if n += utf16.RuneLen(r); n > maxLength {
			return s[:i]
		}
	}
	return s
}

// readString reads a UTF-16BE string prefixed with its length in code units.
func readString(r io.Reader, maxLength int) (string, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", noEOF(err)
	}
	if int(length) > maxLength {
		return "", fmt.Errorf("string length %d exceeds %d", length, maxLength)
	}
	units := make([]uint16, length)
	if err := binary.Read(r, binary.BigEndian, units); err != nil {
		return "", noEOF(err)
	}
	return string(utf16.Decode(units)), nil
}

// maxComponentDepth bounds the nesting of the components that are converted,
// as each level decodes the rest of the component again: deeper components
// have no text. Real components nest a few levels.
const maxComponentDepth = 64

// TextFromComponent converts a JSON text component to legacy text: its texts,
// formatted with the § codes they contain, but without the styles defined in
// JSON. Missing (nil) or invalid components have no text.
func TextFromComponent(component json.RawMessage) string {
	var b strings.Builder
	appendComponentText(&b, component, 0)
	return b.String()
}

// appendComponentText decodes component, at depth, once, as the kind its
// first byte tells.
func appendComponentText(b *strings.Builder, component json.RawMessage, depth int) {
	component = bytes.TrimLeft(component, " \t\r\n")
	if len(component) == 0 || depth > maxComponentDepth {
		return
	}
	switch component[0] {
	case '"':
		var text string
		if json.Unmarshal(component, &text) == nil {
			b.WriteString(text)
		}
	case '[':
		var list []json.RawMessage
		if json.Unmarshal(component, &list) == nil {
			for _, c := range list {
				appendComponentText(b, c, depth+1)
			}
		}
	case '{':
		var object struct {
			Text  string            `json:"text"`
			Extra []json.RawMessage `json:"extra"`
		}
		if json.Unmarshal(component, &object) == nil {
			b.WriteString(object.Text)
			for _, c := range object.Extra {
				appendComponentText(b, c, depth+1)
			}
		}
	}
}

// readPort reads a port, sent as an int.
func readPort(r io.Reader) (uint16, error) {
	var port int32
	if err := binary.Read(r, binary.BigEndian, &port); err != nil {
		return 0, noEOF(err)
	}
	if port < 0 || port > math.MaxUint16 {
		return 0, fmt.Errorf("port %d out of range", port)
	}
	return uint16(port), nil
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
