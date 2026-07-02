package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// markdown renderers are cached per wrap-width (creating one is not free).
var (
	mdMu    sync.Mutex
	mdCache = map[int]*glamour.TermRenderer{}
)

func mdRenderer(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if r, ok := mdCache[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdCache[width] = r
	return r
}

// renderMarkdown renders agent replies as terminal markdown (dark theme, wrapped
// to width). Falls back to the raw text if rendering isn't possible.
func renderMarkdown(md string, width int) string {
	if width < 20 {
		width = 20
	}
	r := mdRenderer(width)
	if r == nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}
