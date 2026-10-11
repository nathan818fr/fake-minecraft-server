package server

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
)

func proxyHeader(src string) []byte {
	addr := netip.MustParseAddrPort(src)
	h := []byte("\r\n\r\n\x00\r\nQUIT\n\x21\x11\x00\x0c")
	h = append(h, addr.Addr().AsSlice()...)
	h = append(h, 127, 0, 0, 1)
	h = binary.BigEndian.AppendUint16(h, addr.Port())
	return binary.BigEndian.AppendUint16(h, 25565)
}

// testProxyProtocol are the options of listeners with the PROXY protocol,
// trusting the test clients.
var testProxyProtocol = ListenerOptions{ProxyProtocol: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}

func TestProxyProtocol(t *testing.T) {
	cfg := testConfig
	cfg.MaxConnectionsPerIP = 1
	addr := startProxiedServer(t, cfg)

	t.Run("status", func(t *testing.T) {
		c := dial(t, addr)
		c.write(proxyHeader("192.0.2.1:56324"))
		c.statusExchange(777)
	})

	t.Run("health check", func(t *testing.T) {
		c := dial(t, addr)
		c.write([]byte("\r\n\r\n\x00\r\nQUIT\n\x20\x00\x00\x00"))
		c.statusExchange(777)
	})

	t.Run("missing header", func(t *testing.T) {
		c := dial(t, addr)
		c.handshake(777, modernproto.IntentStatus)
		c.expectClosed()
	})

	// All connections come from the proxy, so clients must be limited by the
	// address in the header.
	t.Run("per-client limit", func(t *testing.T) {
		idle := dial(t, addr)
		idle.write(proxyHeader("192.0.2.1:50000"))
		idle.write([]byte{0x10})
		time.Sleep(50 * time.Millisecond)

		other := dial(t, addr)
		other.write(proxyHeader("192.0.2.2:50000"))
		other.statusExchange(777)

		same := dial(t, addr)
		same.write(proxyHeader("192.0.2.1:50001"))
		same.expectClosed()
	})
}

func TestProxyProtocolTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trusted string
		allowed bool
	}{
		{"trusted", "127.0.0.0/8", true},
		{"untrusted", "192.0.2.0/24", false},
		{"none trusted", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := ListenerOptions{ProxyProtocol: true}
			if tc.trusted != "" {
				options.TrustedProxies = []netip.Prefix{netip.MustParsePrefix(tc.trusted)}
			}
			var logs bytes.Buffer
			addr, stop := startServerWithOptions(t, testConfig, slog.New(logging.NewTextHandler(&logs, nil)), options)
			if !tc.allowed {
				expectRejected(t, addr) // Without waiting for the header.
				stop()
				if want := `error="not a trusted proxy, see trusted_proxies"`; !strings.Contains(logs.String(), want) {
					t.Errorf("logs = %q, want them to contain %q", logs.String(), want)
				}
				return
			}
			c := dial(t, addr)
			c.write(proxyHeader("192.0.2.1:56324"))
			c.statusExchange(777)
		})
	}
}

// Each listener has its own options: only the ones with the PROXY protocol
// require a header.
func TestProxyProtocolPerListener(t *testing.T) {
	direct, proxied := listenTCP(t), listenTCP(t)
	serve(t, testConfig, slog.New(slog.DiscardHandler), Listener{Listener: direct}, Listener{Listener: proxied, Options: testProxyProtocol})

	dial(t, direct.Addr().String()).statusExchange(777)
	c := dial(t, proxied.Addr().String())
	c.write(proxyHeader("192.0.2.1:56324"))
	c.statusExchange(777)
	c = dial(t, proxied.Addr().String())
	c.handshake(777, modernproto.IntentStatus)
	c.expectClosed()
}

func TestProxyProtocolDisabledRejectsHeader(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	c.write(proxyHeader("192.0.2.1:56324"))
	c.expectClosed()
}
