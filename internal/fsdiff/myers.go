package fsdiff

import "strings"

// edit is one step of a line-level diff.
type opKind uint8

const (
	opEq  opKind = iota // context line (same on both sides)
	opDel               // line present in a, removed
	opIns               // line present in b, added
)

type edit struct {
	kind opKind
	line string
}

// splitLines splits data into lines without their newline characters. The
// bool reports whether the data ended with a newline (needed to emit git's
// "\ No newline at end of file" marker correctly).
func splitLines(data []byte) ([]string, bool) {
	if len(data) == 0 {
		return nil, true
	}
	s := string(data)
	trailing := strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil, trailing
	}
	return strings.Split(s, "\n"), trailing
}

// lineEdits diffs a against b line-by-line. Common prefix/suffix is trimmed
// first; the remainder goes through Myers' O(ND) algorithm with a size guard
// that degrades to a whole-file replacement for pathological inputs.
func lineEdits(a, b []string) []edit {
	p, s := commonPreSuf(a, b)
	head := a[:p]
	tail := a[len(a)-s:]
	midA, midB := a[p:len(a)-s], b[p:len(b)-s]

	var out []edit
	for _, l := range head {
		out = append(out, edit{opEq, l})
	}
	out = append(out, diffMiddle(midA, midB)...)
	for _, l := range tail {
		out = append(out, edit{opEq, l})
	}
	return out
}

// commonPreSuf returns the lengths of the shared prefix and suffix.
func commonPreSuf(a, b []string) (pre, suf int) {
	n, m := len(a), len(b)
	for pre < n && pre < m && a[pre] == b[pre] {
		pre++
	}
	for suf < n-pre && suf < m-pre && a[n-1-suf] == b[m-1-suf] {
		suf++
	}
	return pre, suf
}

// Bounds for running full Myers with trace backtracking. Inputs larger than
// these (rare: common prefix/suffix trimming handles almost everything)
// degrade to a single delete-all/insert-all block — correct, just not minimal.
const (
	maxMyersLen   = 1024
	maxMyersCells = 1 << 20
)

func diffMiddle(a, b []string) []edit {
	n, m := len(a), len(b)
	switch {
	case n == 0 && m == 0:
		return nil
	case n == 0:
		return insAll(b)
	case m == 0:
		return delAll(a)
	}
	if n > maxMyersLen || m > maxMyersLen || n*m > maxMyersCells {
		return append(delAll(a), insAll(b)...)
	}
	return myers(a, b)
}

func delAll(a []string) []edit {
	out := make([]edit, len(a))
	for i, l := range a {
		out[i] = edit{opDel, l}
	}
	return out
}

func insAll(b []string) []edit {
	out := make([]edit, len(b))
	for i, l := range b {
		out[i] = edit{opIns, l}
	}
	return out
}

// myers computes the minimal edit script between a and b using Myers' O((N+M)D)
// shortest-edit-distance algorithm with full V-trace backtracking.
func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	maxD := n + m
	offset := maxD
	v := make([]int, 2*maxD+1)

	var trace [][]int
	found := false
	for d := 0; d <= maxD && !found; d++ {
		vc := make([]int, len(v))
		copy(vc, v)
		trace = append(trace, vc)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1] // down: insertion from b
			} else {
				x = v[offset+k-1] + 1 // right: deletion from a
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}

	// Backtrack through the trace collecting edits (in reverse).
	var rev []edit
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[offset+k-1] < vv[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY { // snake (equal lines)
			x--
			y--
			rev = append(rev, edit{opEq, a[x]})
		}
		if d > 0 {
			if x == prevX { // came down ⇒ insertion
				y--
				rev = append(rev, edit{opIns, b[y]})
			} else { // came right ⇒ deletion
				x--
				rev = append(rev, edit{opDel, a[x]})
			}
		}
	}
	// Reverse into forward order.
	out := make([]edit, len(rev))
	for i, e := range rev {
		out[len(rev)-1-i] = e
	}
	return out
}
