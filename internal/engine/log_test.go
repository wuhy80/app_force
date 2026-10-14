package engine

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
)

func TestLogsRemoveTerminalFormatting(t *testing.T) {
	want := "DEBUG[0349] [1376921691 0ms] router: match[4] ip_is_private=true => route(direct)"
	for _, test := range []struct {
		name, message, want string
	}{
		{"kernel ANSI colors", "\x1b[37mDEBUG\x1b[0m[0349] [\x1b[38;5;107m1376921691\x1b[0m 0ms] router: match[4] ip_is_private=true => route(direct)", want},
		{"visible escape symbols", "␛[37mDEBUG␛[0m[0349] [␛[38;5;107m1376921691␛[0m 0ms] router: match[4] ip_is_private=true => route(direct)", want},
		{"true color and emphasis", "\x1b[1;38;2;255;128;0m连接超时\x1b[0m", "连接超时"},
		{"plain brackets paths and newlines", "router: match[4] => route(direct)\nC:\\Program Files\\AppForce\\app.exe\t[0ms]", "router: match[4] => route(direct)\nC:\\Program Files\\AppForce\\app.exe\t[0ms]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs Logs
			logs.WriteMessage(log.LevelDebug, test.message)
			entry := logs.Entries()[0]
			if entry.Message != test.want {
				t.Fatalf("message = %q, want %q", entry.Message, test.want)
			}
			if entry.Level != "debug" || entry.Time.IsZero() {
				t.Fatalf("lost log metadata: %+v", entry)
			}
		})
	}
}

func TestSingBoxPlatformLogsArePlainText(t *testing.T) {
	var logs Logs
	now := time.Now()
	factory, err := log.New(log.Options{
		Context: context.Background(), Options: option.LogOptions{Level: "debug", DisableColor: true},
		DefaultWriter: io.Discard, BaseTime: now, PlatformWriter: &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	ctx := log.ContextWithID(context.Background(), log.ID{ID: 1376921691, CreatedAt: now})
	factory.NewLogger("router").DebugContext(ctx, "match[4] ip_is_private=true => route(direct)")
	entries := logs.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	message := entries[0].Message
	if strings.ContainsAny(message, "\x1b␛") || !strings.Contains(message, "1376921691") || !strings.Contains(message, "router: match[4] ip_is_private=true => route(direct)") {
		t.Fatalf("unexpected platform log: %q", message)
	}
}
