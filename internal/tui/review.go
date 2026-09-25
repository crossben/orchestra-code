package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/validate"
)

// fileDiff is one file's section of a unified diff.
type fileDiff struct {
	Path    string
	Status  byte // 'A' added, 'M' modified, 'D' deleted, 'R' renamed
	Binary  bool
	Added   int
	Removed int
	Body    string // the section, from its "diff --git" line
}

// parseDiff splits a git-style unified diff (git or fsdiff output) into files.
func parseDiff(diff string) []fileDiff {
	var (
		files  []fileDiff
		cur    *fileDiff
		body   strings.Builder
		inHunk bool
	)
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimRight(body.String(), "\n")
			files = append(files, *cur)
		}
		body.Reset()
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			cur = &fileDiff{Status: 'M', Path: pathFromHeader(line)}
			inHunk = false
		}
		if cur == nil {
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			cur.Added++
		case inHunk && strings.HasPrefix(line, "-"):
			cur.Removed++
		case inHunk:
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = 'A'
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = 'D'
		case strings.HasPrefix(line, "rename to "):
			cur.Status = 'R'
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files"), strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "+++ ") && !strings.HasSuffix(line, "/dev/null"):
			cur.Path = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		}
	}
	flush()
	return files
}

// hunks drops a file section's git header lines (diff --git, index, ---/+++):
// the pane title already names the file and its status.
func hunks(body string) string {
	if i := strings.Index(body, "\n@@"); i >= 0 {
		return body[i+1:]
	}
	return body
}

// pathFromHeader takes the b/ path from "diff --git a/x b/x".
func pathFromHeader(line string) string {
	if i := strings.LastIndex(line, " b/"); i >= 0 {
		return line[i+3:]
	}
	return strings.TrimPrefix(line, "diff --git ")
}

func diffTotals(files []fileDiff) (added, removed int) {
	for _, f := range files {
		added += f.Added
		removed += f.Removed
	}
	return added, removed
}

// counts renders "+12 −3" in green/red, omitting zero sides.
func counts(added, removed int) string {
	var parts []string
	if added > 0 || removed == 0 {
		parts = append(parts, addSty.Render(fmt.Sprintf("+%d", added)))
	}
	if removed > 0 {
		parts = append(parts, delSty.Render(fmt.Sprintf("−%d", removed)))
	}
	return strings.Join(parts, " ")
}

func statusGlyph(s byte) string {
	switch s {
	case 'A':
		return addSty.Render("A")
	case 'D':
		return delSty.Render("D")
	case 'R':
		return warnSty.Render("R")
	}
	return youSty.Render("M")
}

// reviewMeta describes where a diff came from, for the summary bar.
type reviewMeta struct {
	Agent    string
	Task     string
	Attempts int
	Max      int              // 0 = unknown
	Report   *validate.Report // nil = not available (history)
	Outcome  string           // history outcome; "" while pending review
	When     time.Time
	ReadOnly bool // no accept/reject (Changes, History)
}

// reviewer is the file-by-file diff review screen shared by Chat, Changes and
// History: a file list on the left, the selected file's highlighted diff on
// the right, and a summary bar with validation results on top.
type reviewer struct {
	meta  reviewMeta
	files []fileDiff
	full  string
	sel   int
	all   bool // show every file in one scroll instead of one at a time
	vp    viewport.Model
	w, h  int
}

const fileListWidth = 34

func newReviewer(diff string, meta reviewMeta, w, h int) reviewer {
	r := reviewer{meta: meta, full: diff, files: parseDiff(diff), vp: viewport.New(w, h)}
	r.setSize(w, h)
	return r
}

func (r reviewer) showList() bool { return !r.all && len(r.files) > 1 && r.w >= 90 }

func (r *reviewer) setSize(w, h int) {
	r.w, r.h = w, h
	paneW := w
	if r.showList() {
		paneW = w - fileListWidth - 1
	}
	r.vp.Width = max(paneW-4, 10)            // border + padding
	r.vp.Height = max(h-r.chromeHeight(), 3) // summary, failure tail, pane borders + title
	r.refresh()
}

// chromeHeight is the number of lines around the diff viewport.
func (r reviewer) chromeHeight() int {
	return 2 + len(r.failureTail()) + 3
}

func (r *reviewer) refresh() {
	switch {
	case len(r.files) == 0:
		r.vp.SetContent(dimSty.Render(r.full))
	case r.all:
		r.vp.SetContent(highlightDiff(r.full, r.vp.Width))
	default:
		f := r.files[r.sel]
		if f.Binary {
			r.vp.SetContent(dimSty.Render("binary file — no text diff"))
		} else {
			r.vp.SetContent(highlightDiff(hunks(f.Body), r.vp.Width))
		}
	}
	r.vp.GotoTop()
}

// update handles review navigation keys; handled=false lets the caller act.
func (r reviewer) update(msg tea.KeyMsg) (reviewer, tea.Cmd, bool) {
	switch msg.String() {
	case "]", "J":
		if r.sel < len(r.files)-1 {
			r.sel++
			r.all = false
			r.setSize(r.w, r.h)
		}
		return r, nil, true
	case "[", "K":
		if r.sel > 0 {
			r.sel--
			r.all = false
			r.setSize(r.w, r.h)
		}
		return r, nil, true
	case "a":
		r.all = !r.all
		r.setSize(r.w, r.h)
		return r, nil, true
	case "g", "home":
		r.vp.GotoTop()
		return r, nil, true
	case "G", "end":
		r.vp.GotoBottom()
		return r, nil, true
	case "up", "down", "j", "k", "pgup", "pgdown", "ctrl+u", "ctrl+d", "f", "b", " ":
		var cmd tea.Cmd
		r.vp, cmd = r.vp.Update(msg)
		return r, cmd, true
	}
	return r, nil, false
}

func (r reviewer) hints() []string {
	h := []string{}
	if !r.meta.ReadOnly {
		h = append(h, "y accept", "n reject")
	} else {
		h = append(h, "esc back")
	}
	if len(r.files) > 1 {
		h = append(h, "[ ] file", "a all files")
	}
	return append(h, "↑↓ scroll")
}

// failureTail is the last lines of the failing validation stage, shown above
// the diff so a failed run can be judged without leaving the review.
func (r reviewer) failureTail() []string {
	rep := r.meta.Report
	if rep == nil || rep.Skipped || rep.Passed() {
		return nil
	}
	f, ok := rep.Failure()
	if !ok {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(f.Output, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	out := []string{badSty.Render("✗ "+f.Name+" failed") + dimSty.Render(" — last output:")}
	for _, l := range lines {
		out = append(out, faintSty.Render("│ ")+dimSty.Render(ansiTruncate(l, r.w-4)))
	}
	return out
}

func (r reviewer) summary() string {
	var left []string
	switch r.meta.Outcome {
	case "":
		left = append(left, pill("REVIEW", pillBg))
	case "accepted":
		left = append(left, pill("ACCEPTED", green))
	case "rejected":
		left = append(left, pill("REJECTED", red))
	default:
		left = append(left, pill(strings.ToUpper(r.meta.Outcome), gray))
	}
	if r.meta.Agent != "" {
		left = append(left, youSty.Render(r.meta.Agent))
	}
	if r.meta.Max > 1 {
		left = append(left, dimSty.Render(fmt.Sprintf("attempt %d/%d", r.meta.Attempts, r.meta.Max)))
	} else if r.meta.Attempts > 1 {
		left = append(left, dimSty.Render(fmt.Sprintf("%d attempts", r.meta.Attempts)))
	}
	if b := validationBadges(r.meta.Report); b != "" {
		left = append(left, b)
	}
	if !r.meta.When.IsZero() {
		left = append(left, dimSty.Render(r.meta.When.Local().Format("Jan 2 15:04")))
	}
	a, d := diffTotals(r.files)
	right := dimSty.Render(fmt.Sprintf("%d file%s  ", len(r.files), plural(len(r.files)))) + counts(a, d)
	line := left[0] + " " + strings.Join(left[1:], dimSty.Render(" · "))
	gap := r.w - lipgloss.Width(line) - lipgloss.Width(right)
	if gap < 1 {
		return line
	}
	return line + strings.Repeat(" ", gap) + right
}

// validationBadges renders "build ✓  lint ✓  test ✗" (or "unverified").
func validationBadges(rep *validate.Report) string {
	if rep == nil {
		return ""
	}
	if rep.Skipped {
		return warnSty.Render("unverified")
	}
	var parts []string
	for _, s := range rep.Stages {
		if s.Passed {
			parts = append(parts, dimSty.Render(s.Name)+" "+okSty.Render("✓"))
		} else {
			parts = append(parts, dimSty.Render(s.Name)+" "+badSty.Render("✗"))
		}
	}
	return strings.Join(parts, "  ")
}

func (r reviewer) fileList(h int) string {
	var b strings.Builder
	start, end := listWindow(len(r.files), r.sel, h)
	inner := fileListWidth - 4
	for i := start; i < end; i++ {
		f := r.files[i]
		c := counts(f.Added, f.Removed)
		nameW := inner - 5 - lipgloss.Width(c)
		name := truncLeft(f.Path, nameW)
		marker := "  "
		if i == r.sel {
			marker = selSty.Render("▸ ")
			name = selSty.Render(name)
		}
		line := marker + statusGlyph(f.Status) + " " + fit(name, nameW) + " " + c
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (r reviewer) view() string {
	var b strings.Builder
	b.WriteString(r.summary())
	b.WriteString("\n")
	if r.meta.Task != "" {
		b.WriteString(dimSty.Render("task: ") + ansiTruncate(firstLine(r.meta.Task), r.w-6))
	}
	b.WriteString("\n")
	for _, l := range r.failureTail() {
		b.WriteString(l + "\n")
	}

	title := "all files"
	if !r.all && len(r.files) > 0 {
		f := r.files[r.sel]
		title = f.Path + "  " + counts(f.Added, f.Removed)
		if len(r.files) > 1 {
			title = fmt.Sprintf("%d/%d  ", r.sel+1, len(r.files)) + title
		}
	}
	paneH := r.vp.Height + 3
	pane := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(r.vp.Width + 2).Height(paneH - 2).
		Render(headSty.Render(ansiTruncate(title, r.vp.Width)) + "\n" + r.vp.View())
	if !r.showList() {
		b.WriteString(pane)
		return b.String()
	}
	list := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).
		Padding(0, 1).Width(fileListWidth - 2).Height(paneH - 2).
		Render(headSty.Render("files") + "\n" + r.fileList(paneH-3))
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, list, " ", pane))
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
