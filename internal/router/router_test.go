package router

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
)

type fakeClassifier struct {
	c   Classification
	err error
}

func (f fakeClassifier) Classify(context.Context, string, string) (Classification, error) {
	return f.c, f.err
}

type fakeHistory struct {
	name, reason string
	ok           bool
	gotDir       string
	gotCands     []string
	calls        int
}

func (f *fakeHistory) BestAgent(dir string, candidates []string) (string, string, bool) {
	f.calls++
	f.gotDir, f.gotCands = dir, candidates
	return f.name, f.reason, f.ok
}

// testRegistry: alpha, beta, gamma are healthy ("true" is on PATH); dead is not.
func testRegistry() *agent.Registry {
	reg := agent.NewRegistry()
	caps := []agent.Capability{agent.CapImplement}
	reg.Add(agent.New("alpha", "true", nil, "", caps))
	reg.Add(agent.New("dead", "orchestra-no-such-binary-xyz", nil, "", caps))
	reg.Add(agent.New("beta", "true", nil, "", caps))
	reg.Add(agent.New("gamma", "true", nil, "", caps))
	return reg
}

func TestRouteResolutionOrder(t *testing.T) {
	routes := map[string]string{"implement": "beta"}
	cases := []struct {
		name       string
		cls        fakeClassifier
		hist       *fakeHistory // nil → no history wired
		wantAgent  string
		wantReason string // substring
		wantNoHist bool   // history must not be consulted
	}{
		{
			name:      "healthy AI suggestion beats history",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement, Agent: "alpha", Reason: "ai"}},
			hist:      &fakeHistory{name: "gamma", reason: "gamma: 5/6 accepted in this dir", ok: true},
			wantAgent: "alpha", wantReason: "ai", wantNoHist: true,
		},
		{
			name:      "unhealthy suggestion falls to history",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement, Agent: "dead", Reason: "ai"}},
			hist:      &fakeHistory{name: "gamma", reason: "gamma: 5/6 accepted in this dir", ok: true},
			wantAgent: "gamma", wantReason: "ai · history: gamma: 5/6 accepted in this dir",
		},
		{
			name:      "history beats static route",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement}},
			hist:      &fakeHistory{name: "alpha", reason: "alpha: 3/3 accepted in this dir", ok: true},
			wantAgent: "alpha", wantReason: "history: alpha: 3/3 accepted in this dir",
		},
		{
			name:      "no history pick uses static route",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement, Reason: "ai"}},
			hist:      &fakeHistory{},
			wantAgent: "beta", wantReason: "ai",
		},
		{
			name:      "unhealthy history pick is ignored",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement}},
			hist:      &fakeHistory{name: "dead", reason: "dead: 9/9", ok: true},
			wantAgent: "beta",
		},
		{
			name:      "nil history keeps previous behaviour",
			cls:       fakeClassifier{c: Classification{Intent: IntentImplement}},
			wantAgent: "beta",
		},
		{
			name:      "no route falls to default agent",
			cls:       fakeClassifier{c: Classification{Intent: IntentReview}},
			hist:      &fakeHistory{},
			wantAgent: "gamma",
		},
		{
			name:      "classification failure still consults history",
			cls:       fakeClassifier{err: errors.New("boom")},
			hist:      &fakeHistory{name: "alpha", reason: "alpha: 4/5 accepted in this dir", ok: true},
			wantAgent: "alpha", wantReason: "history: alpha: 4/5 accepted in this dir",
		},
		{
			name:       "questions never consult history",
			cls:        fakeClassifier{c: Classification{Intent: IntentQuestion, Reason: "asks"}},
			hist:       &fakeHistory{name: "alpha", ok: true},
			wantAgent:  "",
			wantReason: "asks", wantNoHist: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(tc.cls, nil, testRegistry(), routes, "gamma")
			if tc.hist != nil {
				r = r.WithHistory(tc.hist)
			}
			d := r.Route(context.Background(), "do it", "/proj")
			if d.Agent != tc.wantAgent {
				t.Fatalf("agent = %q, want %q (reason %q)", d.Agent, tc.wantAgent, d.Reason)
			}
			if !strings.Contains(d.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", d.Reason, tc.wantReason)
			}
			if tc.hist == nil {
				return
			}
			if tc.wantNoHist {
				if tc.hist.calls != 0 {
					t.Fatalf("history consulted %d times, want 0", tc.hist.calls)
				}
				return
			}
			if tc.hist.calls != 1 || tc.hist.gotDir != "/proj" {
				t.Fatalf("history calls=%d dir=%q", tc.hist.calls, tc.hist.gotDir)
			}
			if want := []string{"alpha", "beta", "gamma"}; !reflect.DeepEqual(tc.hist.gotCands, want) {
				t.Fatalf("candidates = %v, want only healthy %v", tc.hist.gotCands, want)
			}
		})
	}
}
