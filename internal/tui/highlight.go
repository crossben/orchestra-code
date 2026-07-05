package tui

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/sergi/go-diff/diffmatchpatch"
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

// parseDiffFiles extracts file names from a unified diff string.
func parseDiffFiles(diff string) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Split(line, " b/")
			if len(parts) == 2 {
				name := parts[1]
				if !seen[name] {
					seen[name] = true
					files = append(files, name)
				}
			}
		}
	}
	return files
}

// diffStats returns a summary line like "+12 -5" from a unified diff.
func diffStats(diff string) string {
	dmp := diffmatchpatch.New()
	lines := strings.Split(diff, "\n")
	var a, b []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			b = append(b, line[1:])
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			a = append(a, line[1:])
		}
	}
	diffs := dmp.DiffMain(strings.Join(a, "\n"), strings.Join(b, "\n"), false)
	added, removed := 0, 0
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			added += strings.Count(d.Text, "\n")
			if !strings.HasSuffix(d.Text, "\n") {
				added++
			}
		case diffmatchpatch.DiffDelete:
			removed += strings.Count(d.Text, "\n")
			if !strings.HasSuffix(d.Text, "\n") {
				removed++
			}
		}
	}
	if added == 0 && removed == 0 {
		return "no changes"
	}
	return okSty.Render(fmt.Sprintf("+%d", added)) + " " + badSty.Render(fmt.Sprintf("-%d", removed))
}
