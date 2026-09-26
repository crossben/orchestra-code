package agent

import "fmt"

// Usage is the token accounting for one agent run (or, summed, for every
// attempt of a supervised run). API agents fill it from the provider reply;
// CLI agents leave it zero — Known false renders as "unknown", not "free".
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Known        bool // the provider reported token counts
	Priced       bool // CostUSD was computed from configured prices
}

// Add returns the sum of u and o. Known/Priced are true when either side is.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + o.InputTokens,
		OutputTokens: u.OutputTokens + o.OutputTokens,
		CostUSD:      u.CostUSD + o.CostUSD,
		Known:        u.Known || o.Known,
		Priced:       u.Priced || o.Priced,
	}
}

// String is the one-line run summary, e.g. "tokens 12.3k in / 1.1k out ·
// $0.04" — the cost part only when priced. Empty when usage is unknown.
func (u Usage) String() string {
	if !u.Known {
		return ""
	}
	s := fmt.Sprintf("tokens %s in / %s out", FormatTokens(u.InputTokens), FormatTokens(u.OutputTokens))
	if u.Priced {
		s += " · " + FormatCost(u.CostUSD)
	}
	return s
}

// Cost prices a run from per-million-token USD prices. Prices are config-only
// (a built-in table would go stale), so cost is computed only when both are
// set (> 0); priced reports whether it was.
func Cost(inTokens, outTokens int, priceInPerMTok, priceOutPerMTok float64) (usd float64, priced bool) {
	if priceInPerMTok <= 0 || priceOutPerMTok <= 0 {
		return 0, false
	}
	return (float64(inTokens)*priceInPerMTok + float64(outTokens)*priceOutPerMTok) / 1e6, true
}

// FormatTokens renders a token count compactly: 950, 12.3k, 123k, 1.2M.
func FormatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 100_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}

// TokensCell is the compact table cell for stored usage: "12.3k/1.1k" (in/out),
// or "—" when nothing was reported (stored zero = unknown).
func TokensCell(in, out int) string {
	if in == 0 && out == 0 {
		return "—"
	}
	return FormatTokens(in) + "/" + FormatTokens(out)
}

// CostCell is the compact table cell for a stored cost, "—" when not priced
// (stored zero).
func CostCell(usd float64) string {
	if usd <= 0 {
		return "—"
	}
	return FormatCost(usd)
}

// FormatCost renders USD with cents, or four decimals below one cent so a
// cheap run does not read as free.
func FormatCost(usd float64) string {
	if usd > 0 && usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}
