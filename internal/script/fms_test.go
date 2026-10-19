package script

import "testing"

// The buckets are part of the contract: changing them would move clients
// between the splits of the operators.
func TestBucket(t *testing.T) {
	testFms(t, []struct{ expr, want string }{
		{`fms.bucket("", 10)`, "2"},
		{`fms.bucket("Notch", 10)`, "0"},
		{`fms.bucket("motd:Notch", 10)`, "3"},
		{`fms.bucket("203.0.113.7", 100)`, "75"},
		{`fms.bucket("Notch", 1)`, "0"},
		{`fms.bucket("Notch", 0)`, "fms.bucket: n must be at least 1, got 0"},
		{`fms.bucket(25565, 2)`, "fms.bucket: for parameter 1: got int, want string"},
	})
}
