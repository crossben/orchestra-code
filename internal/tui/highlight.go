package tui

import (
	"bytes"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// diffStyle is chosen once (fallback-safe) for a consistent dark look.
var diffStyle = func() *chroma.Style {
	if s := styles.Get("catppuccin-mocha"); s != nil {
		return s
	}
	if s := styles.Get("monokai"); s != nil {
		return s
	}
	return styles.Fallback
}()

// highlightDiff renders a unified diff with chroma's diff lexer (ANSI 256-color).
// Falls back to the raw diff if anything goes wrong, so it can never break the view.
func highlightDiff(src string) string {
	lexer := lexers.Get("diff")
	if lexer == nil {
		return src
	}
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		return src
	}
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, diffStyle, it); err != nil {
		return src
	}
	return buf.String()
}
