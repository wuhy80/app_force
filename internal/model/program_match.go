package model

import (
	"regexp"
	"strings"
)

// RuleView describes the effective scope without persisting generated matchers.
type RuleView struct {
	Rule
	ProcessPathRegex []string `json:"process_path_regex,omitempty"`
}

func (r Rule) View() RuleView {
	return RuleView{Rule: r, ProcessPathRegex: r.ProcessPathRegex()}
}

// ProcessPathRegex is shared by routing, service exclusion and activity display.
// Ordinary executables keep exact path matching.
func (r Rule) ProcessPathRegex() []string {
	if IsAppBundle(r.Path) {
		return []string{"^" + regexp.QuoteMeta(r.Path+"/")}
	}
	// Recognize Windows paths independently of the host OS so the generated
	// Windows rules can also be verified by tests on macOS and Linux.
	programPath := strings.ReplaceAll(r.Path, `\`, "/")
	drivePath := len(programPath) > 3 && programPath[1:3] == ":/" &&
		((programPath[0] >= 'A' && programPath[0] <= 'Z') || (programPath[0] >= 'a' && programPath[0] <= 'z'))
	if !drivePath && !strings.HasPrefix(programPath, "//") {
		return nil
	}
	separator := strings.LastIndex(programPath, "/")
	name := programPath[separator+1:]
	if !strings.EqualFold(name, "Antigravity.exe") && !strings.EqualFold(name, "Antigravity IDE.exe") {
		return nil
	}
	quotePath := func(path string) string {
		return strings.ReplaceAll(regexp.QuoteMeta(path), "/", `[\\/]`)
	}
	// Include only known language server executables within this installation,
	// including versioned subdirectories, without matching unrelated node.exe.
	return []string{
		"(?i)^" + quotePath(programPath) + "$",
		"(?i)^" + quotePath(programPath[:separator+1]) +
			`(?:[^\\/]+[\\/])*(?:language_server_windows_x64|language_server_windows_arm64|language_server)\.exe$`,
	}
}
