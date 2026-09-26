// Package router is Orchestra's AI routing layer: it reads a user message and
// decides what to do with it — answer a plain question directly, or dispatch a
// coding task to the best available agent.
//
// Classification is pluggable via the Classifier interface: CLIClassifier asks
// an agent in query mode; APIClassifier calls a hosted LLM directly over HTTP.
// Neither affects the resolution logic here.
//
// Resolution never blocks: AI suggestion → track record in this directory (when
// a History is wired) → static routes → default agent → first healthy agent.
// Only healthy (installed) agents are chosen.
package router

import (
	"context"
	"fmt"
	"time"

	"github.com/crossben/orchestra-code/internal/agent"
)

// Intent is what the user's message wants.
type Intent string

const (
	IntentQuestion  Intent = "question"  // answer directly, no dispatch
	IntentPlan      Intent = "plan"      // decompose a large task
	IntentImplement Intent = "implement" // write/modify code
	IntentReview    Intent = "review"    // review/critique code
)

// Classification is the raw output of a Classifier, before agent resolution.
type Classification struct {
	Intent Intent `json:"intent"`
	Agent  string `json:"agent"`  // optional agent suggestion
	Reason string `json:"reason"` // short human-readable justification
}

// Decision is the router's resolved verdict for a message.
type Decision struct {
	Intent Intent
	Agent  string // resolved agent for a task (empty for a question)
	Reason string
}

// IsQuestion reports whether the message should be answered rather than dispatched.
func (d Decision) IsQuestion() bool { return d.Intent == IntentQuestion }

// Classifier turns a message into a Classification.
type Classifier interface {
	Classify(ctx context.Context, message, dir string) (Classification, error)
}

// History reports which agent has the best track record in a directory. The
// router uses it as a tie-breaker when the AI makes no usable suggestion.
// Implementations must only return one of candidates, and ok=false when the
// data is too thin or tied. reason explains the pick for the user.
type History interface {
	BestAgent(dir string, candidates []string) (name, reason string, ok bool)
}

// Router combines a classifier with agent resolution and direct answering.
type Router struct {
	cls      Classifier
	answerer agent.Querier     // used to answer plain questions
	reg      *agent.Registry   // to check agent availability
	routes   map[string]string // intent → agent name (static fallback)
	fallback string            // default agent
	history  History           // optional track-record tie-breaker
}

// WithHistory enables history-aware resolution (nil disables it) and returns r.
func (r *Router) WithHistory(h History) *Router {
	r.history = h
	return r
}

// New builds a Router.
func New(cls Classifier, answerer agent.Querier, reg *agent.Registry, routes map[string]string, fallback string) *Router {
	return &Router{cls: cls, answerer: answerer, reg: reg, routes: routes, fallback: fallback}
}

// Classifier returns the classifier in use (useful for diagnostics and tests).
func (r *Router) Classifier() Classifier { return r.cls }

// Route classifies the message and resolves it to a Decision. Classification
// failure is not fatal — it degrades to an implement task on the default agent.
func (r *Router) Route(ctx context.Context, message, dir string) Decision {
	c, err := r.cls.Classify(ctx, message, dir)
	if err != nil {
		name, why := r.resolve(IntentImplement, "", dir)
		return Decision{
			Intent: IntentImplement,
			Agent:  name,
			Reason: joinReason(fmt.Sprintf("classification failed (%v); defaulting to implement", err), why),
		}
	}
	if c.Intent == IntentQuestion {
		return Decision{Intent: IntentQuestion, Reason: c.Reason}
	}
	if !validIntent(c.Intent) {
		c.Intent = IntentImplement
	}
	name, why := r.resolve(c.Intent, c.Agent, dir)
	return Decision{
		Intent: c.Intent,
		Agent:  name,
		Reason: joinReason(c.Reason, why),
	}
}

// joinReason appends a data-driven explanation to the classifier's reason.
func joinReason(base, history string) string {
	switch {
	case history == "":
		return base
	case base == "":
		return "history: " + history
	default:
		return base + " · history: " + history
	}
}

// Answer produces a direct textual answer to a plain question.
func (r *Router) Answer(ctx context.Context, message, dir string, timeout time.Duration) (string, error) {
	if r.answerer == nil {
		return "", fmt.Errorf("no agent available to answer questions")
	}
	prompt := "Answer the following question concisely. Do NOT modify any files.\n\nQuestion: " + message
	task := agent.Task{Prompt: prompt, Dir: dir, Timeout: timeout}
	if q, ok := r.answerer.(agent.QuietQuerier); ok {
		return q.QueryQuiet(ctx, task) // quiet: safe inside the TUI, cleaner in the shell
	}
	return r.answerer.Query(ctx, task)
}

// resolve applies the tiered fallback, choosing only healthy agents. The second
// return value explains a history-driven pick ("" otherwise).
func (r *Router) resolve(intent Intent, suggested, dir string) (string, string) {
	if r.healthy(suggested) {
		return suggested, ""
	}
	var healthy []string
	for _, n := range r.reg.Names() {
		if r.healthy(n) {
			healthy = append(healthy, n)
		}
	}
	if r.history != nil && len(healthy) > 0 {
		if name, why, ok := r.history.BestAgent(dir, healthy); ok && contains(healthy, name) {
			return name, why
		}
	}
	if a := r.routes[string(intent)]; r.healthy(a) {
		return a, ""
	}
	if r.healthy(r.fallback) {
		return r.fallback, ""
	}
	if len(healthy) > 0 {
		return healthy[0], ""
	}
	return r.fallback, "" // nothing healthy; dispatch will surface a clear error
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (r *Router) healthy(name string) bool {
	if name == "" {
		return false
	}
	a, ok := r.reg.Get(name)
	if !ok {
		return false
	}
	return a.Health() == nil
}

func validIntent(i Intent) bool {
	switch i {
	case IntentQuestion, IntentPlan, IntentImplement, IntentReview:
		return true
	}
	return false
}
