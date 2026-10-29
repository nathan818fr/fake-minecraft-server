package proxyproto

import (
	"bufio"
	"bytes"
	"testing"
)

func FuzzReadHeader(f *testing.F) {
	f.Add([]byte("PROXY TCP4 192.0.2.1 198.51.100.1 56324 25565\r\n"))
	f.Add([]byte("\r\n\r\n\x00\r\nQUIT\n\x21\x11\x00\x0c\xc0\x00\x02\x01\xc6\x33\x64\x01\xdc\x04\x63\xdd"))
	f.Add([]byte("PROXY UNKNOWN\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadHeader(bufio.NewReader(bytes.NewReader(data)))
	})
}
