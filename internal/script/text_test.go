package script

import "testing"

func TestTextFunctions(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\nb\tc\x00d\u202ee\u200bf\xffg", "abcdefg"},
		{"§cNotch é 名前", "§cNotch é 名前"},
	}
	for _, tc := range tests {
		if got := stripInvisible(tc.in); got != tc.want {
			t.Errorf("stripInvisible(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	c, err := NewScope(nil, nil).CompileCondition(`fms.sanitize("§c§\nNotch\u202e") == "Notch"`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &sampleBase}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

func TestComponentFunctions(t *testing.T) {
	testFms(t, []struct{ expr, want string }{
		{`fms.to_legacy({"text": "Hub ", "color": "gold", "extra": [{"text": "1.21", "bold": True}]})`, `"§6Hub §l1.21"`},
		{`fms.to_legacy(["§cA", "B"])`, `"§cA§rB"`},
		{`fms.to_legacy("§cText")`, `"§cText"`},
		{`fms.plain_text({"text": "§cHub ", "extra": [{"text": "online", "color": "green"}]})`, `"Hub online"`},
		{`fms.plain_text("§cRed §lbold§r")`, `"Red bold"`},
		{`fms.from_legacy("§6Gold §lbold")`, `{"text": "", "extra": [{"text": "Gold ", "color": "gold"}, {"text": "bold", "color": "gold", "bold": True}]}`},
		{`fms.from_legacy("&#ff8800Hi", char="&")`, `{"text": "Hi", "color": "#ff8800"}`},
		{`fms.from_legacy("Hi") == {"text": "Hi"}`, "True"},
		{`fms.to_legacy(fms.from_legacy("&aA&lB", char="&"))`, `"§aA§lB"`},
		{`fms.to_legacy(1)`, "fms.to_legacy: got int, want text or a text component"},
		{`fms.plain_text({"text": set()})`, `fms.plain_text: "text": set is not a JSON value`},
		{`fms.from_legacy("x", char="&&")`, `fms.from_legacy: char must be one character, got "&&"`},
	})
}
