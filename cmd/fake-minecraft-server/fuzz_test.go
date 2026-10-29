package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// FuzzReloaderListen applies the lists of Unix socket listeners that ops make,
// one after the other, as reloads do, the test closing the listeners left
// out, as the server would. Each list must keep the listeners of the
// addresses it shares with the previous one, and open the others, or fail,
// for duplicate addresses, without opening any.
func FuzzReloaderListen(f *testing.F) {
	f.Add([]byte{0x01, 0x03, 0x16, 0x0c, 0x00, 0x21})
	f.Add([]byte{0x33, 0x0f, 0x44})
	f.Fuzz(func(t *testing.T, ops []byte) {
		dir := t.TempDir()
		names := []string{"a", "b", "c", "d"}
		r := reloader{}
		t.Cleanup(func() {
			for _, l := range r.listeners {
				_ = l.Close()
			}
		})
		for _, op := range ops {
			// Two bits per address: 1 listens, 3 listens twice; the high bit of
			// each pair enables the PROXY protocol for 2.
			var addrs []config.Listener
			for i, name := range names {
				switch op >> (2 * i) & 3 {
				case 1:
					addrs = append(addrs, config.Listener{Network: "unix", Address: filepath.Join(dir, name)})
				case 2:
					addrs = append(addrs, config.Listener{Network: "unix", Address: filepath.Join(dir, name), Options: server.ListenerOptions{ProxyProtocol: true}})
				case 3:
					addrs = append(addrs, config.Listener{Network: "unix", Address: filepath.Join(dir, name)}, config.Listener{Network: "unix", Address: filepath.Join(dir, name)})
				}
			}
			duplicate := false
			for i := range names {
				if op>>(2*i)&3 == 3 {
					duplicate = true
				}
			}

			old := map[string]openListener{}
			for _, l := range r.listeners {
				old[l.settings.Address] = l
			}
			listeners, added, err := r.listen(context.Background(), addrs)
			if duplicate {
				if err == nil {
					t.Fatalf("listen(%v) succeeded with a duplicate address", addrs)
				}
				for _, addr := range addrs {
					if _, ok := old[addr.Address]; ok {
						continue
					}
					if _, err := os.Stat(addr.Address); !os.IsNotExist(err) {
						t.Fatalf("socket %s left after a failed listen: stat error %v", addr.Address, err)
					}
				}
				continue
			}
			if err != nil {
				t.Fatalf("listen(%v) error: %v", addrs, err)
			}

			if len(listeners) != len(addrs) {
				t.Fatalf("listen(%v) = %d listeners", addrs, len(listeners))
			}
			opened := 0
			listed := map[string]bool{}
			for _, l := range listeners {
				listed[l.settings.Address] = true
				prev, kept := old[l.settings.Address]
				if kept && l.Listener.Listener != prev.Listener.Listener {
					t.Errorf("listener of %s reopened", l.settings.Address)
				}
				if !kept {
					opened++
				}
				var want server.ListenerOptions
				for _, addr := range addrs {
					if addr.Address == l.settings.Address {
						want = addr.Options
					}
				}
				if l.Options.ProxyProtocol != want.ProxyProtocol {
					t.Errorf("listener of %s has the PROXY protocol: %v, want %v", l.settings.Address, l.Options.ProxyProtocol, want.ProxyProtocol)
				}
				if _, err := os.Stat(l.settings.Address); err != nil {
					t.Errorf("socket of %s: %v", l.settings.Address, err)
				}
			}
			if opened != len(added) {
				t.Errorf("opened %d listeners, returned %d as added", opened, len(added))
			}

			// What the server does with the listeners left out.
			for address, l := range old {
				if !listed[address] {
					_ = l.Close()
					if _, err := os.Stat(address); !os.IsNotExist(err) {
						t.Errorf("socket %s left after its listener closed: stat error %v", address, err)
					}
				}
			}
			r.listeners = listeners
		}
	})
}
