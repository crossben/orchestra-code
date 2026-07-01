package agent

// Registry holds the agents available in a session, preserving insertion order
// so listings are stable.
type Registry struct {
	byName map[string]Agent
	order  []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Agent{}}
}

// Add registers an agent. A later Add with the same name replaces the earlier
// one but keeps its position in the ordering.
func (r *Registry) Add(a Agent) {
	if _, exists := r.byName[a.Name()]; !exists {
		r.order = append(r.order, a.Name())
	}
	r.byName[a.Name()] = a
}

// Get returns the agent registered under name.
func (r *Registry) Get(name string) (Agent, bool) {
	a, ok := r.byName[name]
	return a, ok
}

// All returns the agents in registration order.
func (r *Registry) All() []Agent {
	out := make([]Agent, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// Names returns the agent names in registration order.
func (r *Registry) Names() []string {
	return slicesClone(r.order)
}

func slicesClone(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}
