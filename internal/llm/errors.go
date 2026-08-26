package llm

import (
	"fmt"
	"strings"
)

// ErrorKind classifies API failures so callers can produce human-actionable
// messages (notably in `orchestra agents --probe` output).
type ErrorKind int

const (
	ErrNetwork   ErrorKind = iota // transport failure, timeout, DNS
	ErrAuth                       // 401/403 — bad or missing key
	ErrBilling                    // 402 / quota exhausted
	ErrRateLimit                  // 429
	ErrRequest                    // 400/404/422 — malformed request or unknown model
	ErrServer                     // 5xx — provider-side failure
)

// Error is a typed API failure.
type Error struct {
	Provider string // "openai" | "anthropic" ("" for generic network failures)
	Kind     ErrorKind
	Status   int    // HTTP status code (0 when the request never got a response)
	Body     string // truncated response body or transport error text
}

// Error implements the error interface.
func (e *Error) Error() string {
	name := e.Provider
	if name == "" {
		name = "llm"
	}
	switch e.Kind {
	case ErrAuth:
		return fmt.Sprintf("%s: auth failed (status %d)", name, e.Status)
	case ErrBilling:
		return fmt.Sprintf("%s: quota/billing error (status %d)", name, e.Status)
	case ErrRateLimit:
		return fmt.Sprintf("%s: rate limited (status %d)", name, e.Status)
	case ErrRequest:
		return fmt.Sprintf("%s: rejected the request (status %d)", name, e.Status)
	case ErrServer:
		if e.Status > 0 {
			return fmt.Sprintf("%s: server error (status %d)", name, e.Status)
		}
		return fmt.Sprintf("%s: bad response: %s", name, e.Body)
	default:
		return fmt.Sprintf("%s: network error: %s", name, e.Body)
	}
}

// UserDetail returns a short, actionable hint suitable for probe output.
func (e *Error) UserDetail() string {
	switch e.Kind {
	case ErrAuth:
		return "check your API key and that it has access to this provider"
	case ErrBilling:
		return "account out of credits or over quota"
	case ErrRateLimit:
		return "rate limited — retry later or lower concurrency"
	case ErrRequest:
		if e.Body != "" {
			return truncate(e.Body, 160)
		}
		return "request rejected — check the model name and api_base"
	case ErrServer:
		return "provider temporarily unavailable"
	default:
		return truncate(e.Body, 160)
	}
}

// kindFromStatus maps an HTTP status code to its failure kind.
func kindFromStatus(status int) ErrorKind {
	switch {
	case status == 401 || status == 403:
		return ErrAuth
	case status == 402:
		return ErrBilling
	case status == 429:
		return ErrRateLimit
	case status >= 500:
		return ErrServer
	default:
		return ErrRequest
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
