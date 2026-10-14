package model

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSelectedProgramUsesRealExecutablePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows privileges")
	}
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real-program")
	if err := os.WriteFile(realPath, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "selected-program")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	rule, err := NormalizeRule(Rule{Path: linkPath, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if rule.Path != resolved || rule.Name != "selected-program" || !rule.MatchesPath(resolved) {
		t.Fatalf("selection did not match the real executable: %+v", rule)
	}
}

func TestAppBundleIncludesOnlyItsOwnExecutables(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("application bundles are macOS-specific")
	}
	rule, err := NormalizeRule(Rule{Path: "/Applications/Browser+.app", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if rule.Name != "Browser+" {
		t.Fatalf("unexpected bundle display name: %q", rule.Name)
	}
	for _, path := range []string{
		"/Applications/Browser+.app/Contents/MacOS/Browser",
		"/Applications/Browser+.app/Contents/Frameworks/Helper.app/Contents/MacOS/Helper",
	} {
		if !rule.MatchesPath(path) {
			t.Fatalf("bundle executable not matched: %s", path)
		}
	}
	for _, path := range []string{"/Applications/Browser+.app-copy/Contents/MacOS/Browser", "/Applications/Other.app/Contents/MacOS/Browser", ""} {
		if rule.MatchesPath(path) {
			t.Fatalf("unrelated executable matched: %s", path)
		}
	}
}

func TestDNSSettingsDefaultToSystemAndValidateCustomResolvers(t *testing.T) {
	settings := DefaultConfig().Settings
	if settings.DNSMode != "system" || settings.StrictRoute {
		t.Fatal("default settings should preserve system DNS without strict routing")
	}
	settings.DNSAddress = ""
	if err := settings.Validate(); err != nil {
		t.Fatalf("system DNS should not require a custom resolver: %v", err)
	}
	for _, mode := range []string{"direct", "proxy"} {
		settings.DNSMode = mode
		if err := settings.Validate(); err == nil {
			t.Fatalf("%s accepted a missing custom resolver", mode)
		}
		settings.DNSAddress = "8.8.8.8"
		if err := settings.Validate(); err != nil {
			t.Fatal(err)
		}
		settings.DNSAddress = ""
	}
	settings.DNSMode = "unsupported"
	if err := settings.Validate(); err == nil {
		t.Fatal("unsupported DNS mode accepted")
	}
}

func TestAntigravityRuleIncludesOnlyItsWindowsLanguageServers(t *testing.T) {
	for _, programPath := range []string{
		`C:\Apps\Antigravity+ (Test)\Antigravity.exe`,
		`D:\Apps\Antigravity IDE\Antigravity IDE.exe`,
		`\\server\apps\Antigravity\Antigravity.exe`,
	} {
		t.Run(programPath, func(t *testing.T) {
			rule := Rule{Path: programPath}
			root := programPath[:strings.LastIndex(programPath, `\`)]
			for _, executable := range []string{
				programPath,
				root + `\language_server_windows_x64.exe`,
				root + `\resources\app\extensions\antigravity\bin\language_server_windows_x64.exe`,
				root + `\resources\version-2.0\language_server.exe`,
				root + `\bin\language_server_windows_arm64.exe`,
				strings.ToUpper(root + `\bin\language_server_windows_x64.exe`),
				strings.ReplaceAll(root+`\bin\language_server.exe`, `\`, "/"),
			} {
				if !rule.MatchesPath(executable) {
					t.Fatalf("selected application executable not matched: %s", executable)
				}
			}
			for _, executable := range []string{
				root + `-copy\language_server_windows_x64.exe`,
				`C:\Other\language_server_windows_x64.exe`,
				root + `\bin\language_server_windows_x64.exe.bak`,
				root + `\bin\not_language_server_windows_x64.exe`,
				root + `\bin\node.exe`,
				root + `\bin\unrelated.exe`,
				"",
			} {
				if rule.MatchesPath(executable) {
					t.Fatalf("unselected application executable matched: %s", executable)
				}
			}
		})
	}
	for _, programPath := range []string{`C:\Apps\Other.exe`, `/opt/Antigravity.exe`, `C:\Apps\language_server_windows_x64.exe`} {
		if expressions := (Rule{Path: programPath}).ProcessPathRegex(); len(expressions) != 0 {
			t.Fatalf("ordinary executable unexpectedly includes helpers: %s", programPath)
		}
	}
}
