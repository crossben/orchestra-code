// Package router is Orchestra's AI routing layer: it reads a user message and
// decides what to do with it — answer a plain question directly, or dispatch a
// coding task to the best available agent.
//
// Classification is pluggable via the Classifier interface. M4 ships a CLI-based
// classifier (an agent in query mode); a direct-API classifier can drop in later
// without touching the resolution logic here.
//
// Resolution is three-tier and never blocks: AI suggestion → static routes →
// default agent. Only healthy (installed) agents are chosen.
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

// Router combines a classifier with agent resolution and direct answering.
type Router struct {
	cls      Classifier
	answerer agent.Querier     // used to answer plain questions
	reg      *agent.Registry   // to check agent availability
	routes   map[string]string // intent → agent name (static fallback)
	fallback string            // default agent
}

// New builds a Router.
func New(cls Classifier, answerer agent.Querier, reg *agent.Registry, routes map[string]string, fallback string) *Router {
	return &Router{cls: cls, answerer: answerer, reg: reg, routes: routes, fallback: fallback}
}

// Route classifies the message and resolves it to a Decision. Classification
// failure is not fatal — it degrades to an implement task on the default agent.
func (r *Router) Route(ctx context.Context, message, dir string) Decision {
	c, err := r.cls.Classify(ctx, message, dir)
	if err != nil {
		return Decision{
			Intent: IntentImplement,
			Agent:  r.resolve(IntentImplement, ""),
			Reason: fmt.Sprintf("classification failed (%v); defaulting to implement", err),
		}
	}
	if c.Intent == IntentQuestion {
		return Decision{Intent: IntentQuestion, Reason: c.Reason}
	}
	if !validIntent(c.Intent) {
		c.Intent = IntentImplement
	}
	return Decision{
		Intent: c.Intent,
		Agent:  r.resolve(c.Intent, c.Agent),
		Reason: c.Reason,
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

// resolve applies the three-tier fallback, choosing only healthy agents.
func (r *Router) resolve(intent Intent, suggested string) string {
	if r.healthy(suggested) {
		return suggested
	}
	if a := r.routes[string(intent)]; r.healthy(a) {
		return a
	}
	if r.healthy(r.fallback) {
		return r.fallback
	}
	for _, n := range r.reg.Names() {
		if r.healthy(n) {
			return n
		}
	}
	return r.fallback // nothing healthy; dispatch will surface a clear error
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
