package agent

import (
	"context"
	"math"
	"testing"

	"github.com/crossben/orchestra-code/internal/llm"
)

func TestCost(t *testing.T) {
	cases := []struct {
		name       string
		in, out    int
		pIn, pOut  float64
		want       float64
		wantPriced bool
	}{
		{"both prices", 1_000_000, 500_000, 3, 15, 3 + 7.5, true},
		{"small run", 12_300, 1_100, 3, 15, 0.0369 + 0.0165, true},
		{"input price only", 1000, 1000, 3, 0, 0, false},
		{"no prices", 1000, 1000, 0, 0, 0, false},
		{"zero tokens priced", 0, 0, 3, 15, 0, true},
	}
	for _, c := range cases {
		got, priced := Cost(c.in, c.out, c.pIn, c.pOut)
		if priced != c.wantPriced || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: Cost = %v,%v want %v,%v", c.name, got, priced, c.want, c.wantPriced)
		}
	}
}

func TestUsageAddAndString(t *testing.T) {
	var total Usage
	if total.String() != "" {
		t.Fatalf("unknown usage should render empty, got %q", total.String())
	}
	total = total.Add(Usage{InputTokens: 12_000, OutputTokens: 600, CostUSD: 0.03, Known: true, Priced: true})
	total = total.Add(Usage{}) // an attempt with no reported usage changes nothing
	total = total.Add(Usage{InputTokens: 300, OutputTokens: 500, CostUSD: 0.01, Known: true, Priced: true})
	if total.InputTokens != 12_300 || total.OutputTokens != 1_100 || !total.Known || !total.Priced {
		t.Fatalf("sum = %+v", total)
	}
	if got, want := total.String(), "tokens 12.3k in / 1.1k out · $0.04"; got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	unpriced := Usage{InputTokens: 950, OutputTokens: 40, Known: true}
	if got, want := unpriced.String(), "tokens 950 in / 40 out"; got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
}

func TestFormatTokensAndCost(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1.0k", 12_345: "12.3k", 123_456: "123k", 1_234_567: "1.2M"} {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", n, got, want)
		}
	}
	for c, want := range map[float64]string{0.04: "$0.04", 1.5: "$1.50", 0.0042: "$0.0042", 0: "$0.00"} {
		if got := FormatCost(c); got != want {
			t.Errorf("FormatCost(%v) = %q, want %q", c, got, want)
		}
	}
}

func TestTableCells(t *testing.T) {
	if TokensCell(0, 0) != "—" || CostCell(0) != "—" {
		t.Fatal("unknown usage should render as —")
	}
	if got := TokensCell(12_345, 1_100); got != "12.3k/1.1k" {
		t.Fatalf("TokensCell = %q", got)
	}
	if got := CostCell(0.042); got != "$0.04" {
		t.Fatalf("CostCell = %q", got)
	}
}

// usageProvider is a fake llm.Provider that reports token usage.
type usageProvider struct {
	fakeProvider
	usage llm.Usage
}

func (u *usageProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	r, err := u.fakeProvider.Complete(ctx, req)
	r.Usage = u.usage
	return r, err
}

func TestAPIAgentRunReportsUsage(t *testing.T) {
	dir := apiRepo(t)
	p := &usageProvider{fakeProvider: fakeProvider{text: diffReply}, usage: llm.Usage{InputTokens: 2_000_000, OutputTokens: 100_000}}

	a := newTestAPIAgent(t, p)
	res, err := a.Run(context.Background(), Task{Prompt: "add a line", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if u := res.Usage; !u.Known || u.Priced || u.InputTokens != 2_000_000 || u.OutputTokens != 100_000 || u.CostUSD != 0 {
		t.Fatalf("unpriced usage = %+v", u)
	}

	a.SetPricing(3, 15)
	p.text = "nothing to change"
	res, err = a.Run(context.Background(), Task{Prompt: "prose please", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if u := res.Usage; !u.Known || !u.Priced || math.Abs(u.CostUSD-7.5) > 1e-9 {
		t.Fatalf("priced usage = %+v", u)
	}
}

func TestAPIAgentRunNoUsageIsUnknown(t *testing.T) {
	a := newTestAPIAgent(t, &fakeProvider{text: "nothing to do"})
	a.SetPricing(3, 15)
	res, err := a.Run(context.Background(), Task{Prompt: "x", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.Known || res.Usage.Priced {
		t.Fatalf("a reply without usage must stay unknown: %+v", res.Usage)
	}
}
