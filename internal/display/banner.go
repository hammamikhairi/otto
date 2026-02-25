package display

import (
	_ "embed"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

//go:embed banner.txt
var bannerRaw string

// RenderBanner returns the banner art horizontally centred for the
// current terminal width. No scaling is applied — the art is displayed
// at its native size. To change the banner just replace banner.txt.
func RenderBanner() string {
	width := termWidth()

	lines := strings.Split(strings.TrimRight(bannerRaw, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}

	// Find the widest line.
	maxW := 0
	for _, l := range lines {
		if len(l) > maxW {
			maxW = len(l)
		}
	}

	var b strings.Builder
	for _, l := range lines {
		pad := 0
		if width > maxW {
			pad = (width - maxW) / 2
		}
		if pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(BannerStyle.Render(l))
		b.WriteByte('\n')
	}
	return b.String()
}

// termWidth returns the current terminal column count, or 80 as fallback.
func termWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	return 80
}
