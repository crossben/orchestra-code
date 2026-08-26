package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// HTTPClient is the subset of *http.Client the providers need. It exists so
// tests can inject a transport without a real network.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultClient returns a client with no global timeout: deadlines come from
// the request's context (agent Task.Timeout), which is how the rest of
// Orchestra (internal/runner) models timeouts.
func defaultClient() HTTPClient {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		},
	}
}

// postJSON is the shared request path for all providers: JSON-encode payload,
// attach headers, POST, decode a 2xx response into out, and map every failure
// to a typed *Error. name is used to attribute errors to their provider.
func postJSON(ctx context.Context, hc HTTPClient, name, url string, headers map[string]string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return &Error{Provider: name, Kind: ErrRequest, Body: "encode request: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &Error{Provider: name, Kind: ErrRequest, Body: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Provider: name, Kind: ErrNetwork, Body: transportText(err)}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{
			Provider: name,
			Kind:     kindFromStatus(resp.StatusCode),
			Status:   resp.StatusCode,
			Body:     truncate(string(raw), bodyDetailLimit),
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Provider: name, Kind: ErrServer, Status: resp.StatusCode, Body: "decode response: " + err.Error()}
	}
	return nil
}

const (
	maxBodyRead     = 1 << 20 // never buffer more than 1 MiB of a response
	bodyDetailLimit = 400     // keep error bodies short but useful
)

// transportText condenses a transport error into its meaningful part: context
// deadlines/cancellation read as themselves, anything else keeps its message.
func transportText(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return truncate(err.Error(), bodyDetailLimit)
	}
}

