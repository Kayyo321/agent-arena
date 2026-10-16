package arena

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func terminalColor(mode string) bool {
	if os.Getenv("NO_COLOR") != "" || mode == "never" {
		return false
	}
	return mode == "always" || os.Getenv("TERM") != "dumb"
}

func unicodeLettering() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if locale := os.Getenv(key); locale != "" {
			locale = strings.ToUpper(locale)
			return strings.Contains(locale, "UTF-8") || strings.Contains(locale, "UTF8")
		}
	}
	return true
}

// Braille dots pack the lettering into three rows without a filled background.
func wordmark(width, height int, mode string, color bool) []string {
	if mode == "hide" {
		return nil
	}
	if mode == "compact" || width < 60 || height < 18 || !unicodeLettering() {
		label := "A2rena"
		if color {
			label = "\x1b[38;5;141m" + label + "\x1b[0m"
		}
		return []string{label}
	}
	letters := [][6]string{
		{"01110", "11011", "11011", "11111", "11011", "11011"}, // A
		{"11110", "00011", "00110", "01100", "11000", "11111"}, // 2
		{"000", "000", "110", "101", "100", "100"},             // r
		{"000", "000", "011", "101", "110", "011"},             // e
		{"000", "000", "000", "110", "101", "101"},             // n
		{"000", "000", "011", "001", "111", "101"},             // a
	}
	palette := []int{183, 141, 99}
	lines := []string{}
	for y := 0; y < len(letters[0]); y += 2 {
		var line strings.Builder
		if color {
			fmt.Fprintf(&line, "\x1b[38;5;%dm", palette[y/2])
		}
		for i, letter := range letters {
			if i > 0 {
				line.WriteByte(' ')
			}
			for x := range len(letter[0]) {
				var dots rune
				if letter[y][x] == '1' {
					dots |= 0x1b // Two upper dots in each column.
				}
				if y+1 < len(letter) && letter[y+1][x] == '1' {
					dots |= 0xe4 // Two lower dots in each column.
				}
				if dots == 0 {
					line.WriteByte(' ')
				} else {
					line.WriteRune(0x2800 + dots)
				}
			}
		}
		if color {
			line.WriteString("\x1b[0m")
		}
		lines = append(lines, line.String())
	}
	return lines
}

// The brand leads from the left margin with its text beside it, matching the
// live status band; a hairline closes the header when there is room.
func brandedHeader(left []string, width, height int, mode string, color bool) []string {
	logo := wordmark(width, height, mode, color)
	logoWidth := 0
	for _, line := range logo {
		logoWidth = max(logoWidth, ansi.StringWidth(line))
	}
	if logoWidth == 0 {
		lines := []string{}
		for _, l := range left {
			lines = append(lines, short(l, width))
		}
		return lines
	}
	margin, gap := 2, 4
	if logoWidth+margin+gap > width {
		margin, gap = 0, 0
	}
	lines := []string{}
	for i := range max(len(left), len(logo)) {
		l, r := "", ""
		if i < len(left) {
			l = styled(left[i], sgrSoft, color && i > 0)
			if i == 0 {
				l = styled(left[i], sgrStrong, color)
			}
		}
		if i < len(logo) {
			r = logo[i]
		}
		lines = append(lines, short(strings.Repeat(" ", margin)+pad(r, logoWidth)+strings.Repeat(" ", gap)+l, width))
	}
	if len(logo) == 3 && height >= 12 {
		rule := "─"
		if !unicodeLettering() {
			rule = "-"
		}
		lead := min(width, margin+logoWidth)
		lines = append(lines, styled(strings.Repeat(rule, lead), "38;5;60", color)+styled(strings.Repeat(rule, max(0, width-lead)), sgrHairline, color))
	}
	return lines
}
