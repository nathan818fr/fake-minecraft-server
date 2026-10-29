package config

import "testing"

func FuzzVariables(f *testing.F) {
	f.Add("SERVERS_DEFAULT", `{ status: { description: Hi, players: { max: 5 } }, kick: Bye }`)
	f.Add("RULES", `[{ name: a, host: [.a.com], when: 'protocol < 47', server: default }]`)
	f.Add("UPSTREAMS_LOBBY", `{ target: lobby.example.com, interval: 10s }`)
	f.Add("LISTEN", ":25565, unix:/tmp/x.sock")
	f.Add("PROXY_PROTOCOL_TRUSTED", "10.0.0.0/8")
	f.Fuzz(func(t *testing.T, name, value string) {
		_, _ = Load([]string{name + "=" + value})
	})
}
