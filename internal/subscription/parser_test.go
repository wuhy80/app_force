package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseFormatsAndIdentity(t *testing.T) {
	link := "ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-128-gcm:p:a:ss")) + "@[2001:db8::1]:443#IPv6"
	vmess := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"v":"2","ps":"VMess","add":"example.org","port":"443","id":"d342d11e-d424-4583-b36e-524ab1f0afa4","aid":"0","net":"ws","type":"none","path":"/ws","host":"cdn.example.org","tls":"tls","sni":"cdn.example.org"}`))
	for _, fixture := range []struct{ name, content, typ string }{
		{"ss IPv6", link, "shadowsocks"},
		{"encoded links", base64.StdEncoding.EncodeToString([]byte(link)), "shadowsocks"},
		{"vmess", vmess, "vmess"},
		{"vless reality", "vless://d342d11e-d424-4583-b36e-524ab1f0afa4@example.org:443?security=reality&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&type=grpc&serviceName=proxy#Reality", "vless"},
		{"trojan", "trojan://p%40ss@example.org:443?sni=example.org#Trojan", "trojan"},
		{"hy2", "hysteria2://password@example.org:443?obfs=salamander&obfs-password=secret#HY2", "hysteria2"},
		{"socks no authentication", "socks5://127.0.0.1:1080#SOCKS", "socks"},
		{"clash", "proxies:\n  - name: Tokyo\n    type: ss\n    server: example.org\n    port: 443\n    cipher: aes-128-gcm\n    password: secret\n", "shadowsocks"},
		{"sing-box", `{"outbounds":[{"type":"direct","tag":"direct"},{"type":"socks","tag":"SOCKS","server":"127.0.0.1","server_port":1080}]}`, "socks"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result, err := Parse([]byte(fixture.content), "source")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Nodes) != 1 || result.Nodes[0].Type != fixture.typ {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(result.Warnings) != 0 {
				t.Fatal(result.Warnings)
			}
			second, err := Parse([]byte(fixture.content), "source")
			if err != nil {
				t.Fatal(err)
			}
			if result.Nodes[0].ID != second.Nodes[0].ID {
				t.Fatal("refresh changed node identity")
			}
		})
	}
}

func TestPartialImportAndRejectUnsupportedFields(t *testing.T) {
	data := "trojan://secret@example.org:443#good\nvless://id@example.org:443?type=xhttp#bad\nss://invalid"
	result, err := Parse([]byte(data), "source")
	if err != nil || len(result.Nodes) != 1 || len(result.Warnings) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, content := range []string{
		`{"type":"socks","server":"localhost","server_port":1080,"detour":"missing"}`,
		`{"type":"trojan","server":"localhost","server_port":443,"password":"secret","tls":{"enabled":true,"certificate_path":"/etc/secret"}}`,
		"proxies:\n - name: invalid\n   type: ss\n   server: localhost\n   port: 443\n   cipher: aes-128-gcm\n   password: secret\n   plugin: obfs\n",
		"vless://uuid@example.org:443?unknown=1",
	} {
		if _, err := Parse([]byte(content), "source"); err == nil {
			t.Fatalf("accepted unsupported content: %s", content)
		}
	}
}

func TestShadowsocksPasswordPreservedAndDeduplicated(t *testing.T) {
	link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p:a:ss")) + "@host.example:443#Name"
	result, err := Parse([]byte(link+"\n"+link), "source")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 1 {
		t.Fatal("duplicate node not removed")
	}
	var outbound map[string]any
	if err := json.Unmarshal(result.Nodes[0].Outbound, &outbound); err != nil {
		t.Fatal(err)
	}
	if outbound["password"] != "p:a:ss" {
		t.Fatal("password corrupted")
	}
	if _, err := Parse([]byte(strings.Repeat("a", MaxSize+1)), "source"); err == nil {
		t.Fatal("oversized subscription accepted")
	}
}
