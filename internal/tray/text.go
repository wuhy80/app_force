package tray

import (
	"strings"
	"unicode/utf16"
)

// Node names are subscription content, not Win32 accelerator or control markup.
func menuText(text string) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, text)
	return strings.ReplaceAll(text, "&", "&&")
}

func tooltip(text string) (tip [128]uint16) {
	units := utf16.Encode([]rune(strings.ReplaceAll(text, "\x00", " ")))
	if len(units) > 127 {
		units = units[:127]
		if units[126] >= 0xd800 && units[126] <= 0xdbff {
			units = units[:126]
		}
	}
	copy(tip[:], units)
	return tip
}
