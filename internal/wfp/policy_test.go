package wfp

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"appforce/internal/model"
)

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := Compile([]model.Rule{
		{Path: `C:\Apps\Antigravity\Antigravity.exe`, Enabled: true, NodeID: "pinned"},
		{Path: `C:\Tools\chosen.exe`, Enabled: true},
		{Path: `C:\Tools\builder.exe`, Enabled: false},
	}, func(path string) (string, error) { return `\device\harddiskvolume3` + strings.ToLower(path[2:]), nil })
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func contextBytes(p *Policy, id uint32, path string, ipv6 bool) []byte {
	b := make([]byte, ContextSize)
	put := binary.LittleEndian.PutUint32
	put(b, Magic)
	put(b[4:], Version)
	put(b[8:], ContextSize)
	put(b[12:], id)
	binary.LittleEndian.PutUint64(b[16:], 1234)
	copy(b[24:], p.Cookie[:])
	binary.LittleEndian.PutUint16(b[42:], 443)
	if ipv6 {
		binary.LittleEndian.PutUint16(b[40:], 6)
		copy(b[48:], []byte{0x20, 0x01, 0x48, 0x60})
	} else {
		binary.LittleEndian.PutUint16(b[40:], 4)
		copy(b[48:], []byte{8, 8, 8, 8})
	}
	chars := utf16.Encode([]rune(path))
	put(b[64:], uint32(len(chars)*2))
	for i, c := range chars {
		binary.LittleEndian.PutUint16(b[72+i*2:], c)
	}
	return b
}

func TestPolicyScopesAndWireContract(t *testing.T) {
	p := testPolicy(t)
	if len(p.Rules) != 2 {
		t.Fatal("disabled executable was captured")
	}
	for path, want := range map[string]bool{
		p.Rules[0].AppID: true,
		`\device\harddiskvolume3\apps\antigravity\resources\v2\language_server_windows_x64.exe`: true,
		`\device\harddiskvolume3\apps\antigravity\node.exe`:                                     false,
		`\device\harddiskvolume3\apps\antigravity\builder.exe`:                                  false,
		`\device\harddiskvolume3\apps\antigravity-other\language_server.exe`:                    false,
		`\device\harddiskvolume4\apps\antigravity\language_server.exe`:                          false,
	} {
		if got := p.Rules[0].Matches(path); got != want {
			t.Fatalf("%s: match=%v", path, got)
		}
	}
	b, err := p.Encode(999, 12345, 12346)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 48+2*2064 || binary.LittleEndian.Uint16(b[24:]) != 12345 || binary.LittleEndian.Uint16(b[26:]) != 12346 || string(b[32:48]) != string(p.Cookie[:]) {
		t.Fatal("driver contract layout changed")
	}
	for _, v6 := range []bool{false, true} {
		data := contextBytes(p, 1, `\device\harddiskvolume3\apps\antigravity\resources\language_server.exe`, v6)
		r, err := p.DecodeContext(data)
		if err != nil {
			t.Fatal(err)
		}
		if r.PID != 1234 || r.Rule.Program.NodeID != "pinned" || r.ProcessPath != `C:\Apps\Antigravity\resources\language_server.exe` || r.Destination.Addr().Is6() != v6 {
			t.Fatalf("lost attribution: %+v", r)
		}
	}
}

func TestRejectForeignStaleMalformedAndUnselectedRedirects(t *testing.T) {
	p := testPolicy(t)
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":     func(b []byte) []byte { return b[:40] },
		"oversized":     func(b []byte) []byte { return append(b, 0) },
		"stale cookie":  func(b []byte) []byte { b[24] ^= 1; return b },
		"version":       func(b []byte) []byte { b[4]++; return b },
		"unknown rule":  func(b []byte) []byte { b[12] = 3; return b },
		"zero rule":     func(b []byte) []byte { b[12] = 0; return b },
		"missing pid":   func(b []byte) []byte { clear(b[16:24]); return b },
		"oversized pid": func(b []byte) []byte { b[23] = 1; return b },
		"path size":     func(b []byte) []byte { b[64] = 1; return b },
		"dns":           func(b []byte) []byte { binary.LittleEndian.PutUint16(b[42:], 53); return b },
		"private":       func(b []byte) []byte { b[48] = 10; return b },
		"loopback":      func(b []byte) []byte { b[48] = 127; return b },
		"scope":         func(b []byte) []byte { b[44] = 1; return b },
		"reserved":      func(b []byte) []byte { b[68] = 1; return b },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := p.DecodeContext(mutate(contextBytes(p, 1, p.Rules[0].AppID, false))); err == nil {
				t.Fatal("accepted invalid driver context")
			}
		})
	}
	if _, err := p.DecodeContext(contextBytes(p, 1, `\device\harddiskvolume3\apps\antigravity\builder.exe`, false)); err == nil {
		t.Fatal("captured build subprocess")
	}
}

func TestPolicyLimits(t *testing.T) {
	for _, appID := range []string{`C:\program.exe`, `\device\` + strings.Repeat("x", MaxPath), "\x00"} {
		if _, err := Compile([]model.Rule{{Path: "x", Enabled: true}}, func(string) (string, error) { return appID, nil }); err == nil {
			t.Fatal("accepted invalid app ID")
		}
	}
	rules := make([]model.Rule, MaxRules+1)
	for i := range rules {
		rules[i] = model.Rule{Path: "x", Enabled: true}
	}
	if _, err := Compile(rules, func(string) (string, error) { return `\device\a\b.exe`, nil }); err == nil {
		t.Fatal("accepted too many rules")
	}
}

func TestHealthRequiresCurrentPolicyAndAllFourFilters(t *testing.T) {
	p := testPolicy(t)
	valid := make([]byte, HealthSize)
	put := binary.LittleEndian.PutUint32
	put(valid, Magic)
	put(valid[4:], Version)
	put(valid[8:], HealthSize)
	put(valid[12:], 1)
	copy(valid[16:32], p.Cookie[:])
	put(valid[32:], uint32(len(p.Rules)))
	put(valid[36:], 4)
	if err := p.CheckHealth(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":      func(b []byte) []byte { return b[:16] },
		"old driver":     func(b []byte) []byte { b[4]--; return b },
		"inactive":       func(b []byte) []byte { b[12] = 0; return b },
		"stale session":  func(b []byte) []byte { b[16] ^= 1; return b },
		"wrong rules":    func(b []byte) []byte { b[32]++; return b },
		"missing filter": func(b []byte) []byte { b[36]--; return b },
	} {
		t.Run(name, func(t *testing.T) {
			if p.CheckHealth(mutate(append([]byte(nil), valid...))) == nil {
				t.Fatal("accepted unhealthy WFP session")
			}
		})
	}
}
