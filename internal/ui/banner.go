package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// bannerArt is the ORCHESTRA wordmark.
var bannerArt = []string{
	" ██████╗ ██████╗  ██████╗██╗  ██╗███████╗███████╗████████╗██████╗  █████╗ ",
	"██╔═══██╗██╔══██╗██╔════╝██║  ██║██╔════╝██╔════╝╚══██╔══╝██╔══██╗██╔══██╗",
	"██║   ██║██████╔╝██║     ███████║█████╗  ███████╗   ██║   ██████╔╝███████║",
	"██║   ██║██╔══██╗██║     ██╔══██║██╔══╝  ╚════██║   ██║   ██╔══██╗██╔══██║",
	"╚██████╔╝██║  ██║╚██████╗██║  ██║███████╗███████║   ██║   ██║  ██║██║  ██║",
	" ╚═════╝ ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚══════╝╚══════╝   ╚═╝   ╚═╝  ╚═╝╚═╝  ╚═╝",
}

// gradient endpoints (cyan → violet).
var (
	gradFrom, _ = colorful.Hex("#06B6D4")
	gradTo, _   = colorful.Hex("#7C3AED")
)

// Banner prints the wordmark with a horizontal color gradient and a subtle
// line-by-line reveal. Falls back to plain text when rich output is disabled.
func Banner(tagline string) {
	if !Enabled {
		fmt.Println("ORCHESTRA")
		if tagline != "" {
			fmt.Println(tagline)
		}
		return
	}
	width := len([]rune(bannerArt[len(bannerArt)-1]))
	for _, line := range bannerArt {
		fmt.Println(gradientLine(line, width))
		time.Sleep(28 * time.Millisecond) // gentle reveal
	}
	if tagline != "" {
		fmt.Println(styDim.Render("  " + tagline))
	}
}

// gradientLine colors each rune by its horizontal position.
func gradientLine(line string, width int) string {
	var b strings.Builder
	runes := []rune(line)
	for i, r := range runes {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		t := float64(i) / float64(max(width-1, 1))
		c := gradFrom.BlendLab(gradTo, t).Clamped()
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c.Hex())).Render(string(r)))
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
