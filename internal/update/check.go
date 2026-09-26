// Package update checks GitHub Releases for a newer Orchestra and replaces the
// running binary with it. It relies on the GoReleaser contract in
// .goreleaser.yaml: archives named orchestra_<version>_<os>_<arch>.tar.gz
// (.zip on windows) holding the orchestra binary, plus a checksums.txt.
//
// Stdlib only; every network endpoint is injectable so tests run offline.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultAPIBase is GitHub's REST API root.
	DefaultAPIBase = "https://api.github.com"
	// DefaultRepo is where Orchestra releases are published.
	DefaultRepo = "crossben/orchestra-code"
	// CheckInterval throttles automatic checks.
	CheckInterval = 24 * time.Hour

	defaultMaxDownload = 256 << 20 // archive cap
	maxMetadata        = 1 << 20   // API JSON / checksums.txt cap
)

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the latest published release.
type Release struct {
	Tag        string  // e.g. "v0.9.0"
	Version    string  // Tag without the leading "v" (the GoReleaser .Version)
	Prerelease bool    // GitHub's prerelease flag
	Assets     []Asset // archives + checksums.txt
}

type ghRelease struct {
	TagName    string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Client talks to the release host. The zero value uses GitHub and the running
// platform; tests override the fields.
type Client struct {
	APIBase     string       // default DefaultAPIBase
	Repo        string       // default DefaultRepo
	HTTP        *http.Client // default: a client with a 5 minute ceiling
	GOOS        string       // default runtime.GOOS
	GOARCH      string       // default runtime.GOARCH
	UserAgent   string       // default "orchestra-updater"
	MaxDownload int64        // archive size cap in bytes; default 256 MiB
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return DefaultAPIBase
}

func (c *Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *Client) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Client) goarch() string {
	if c.GOARCH != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

func (c *Client) maxDownload() int64 {
	if c.MaxDownload > 0 {
		return c.MaxDownload
	}
	return defaultMaxDownload
}

// get performs a GET and returns the body, capped at limit bytes.
func (c *Client) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "orchestra-updater"
	}
	req.Header.Set("User-Agent", ua)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s: response exceeds the %d byte limit", url, limit)
	}
	return body, nil
}

// Check fetches the latest release and reports whether it is newer than
// current. Uncheckable builds (see Checkable) return immediately without any
// network access. A prerelease is never offered to a stable build.
func (c *Client) Check(ctx context.Context, current string) (Release, bool, error) {
	if !Checkable(current) {
		return Release{}, false, nil
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase(), c.repo())
	body, err := c.get(ctx, url, "application/vnd.github+json", maxMetadata)
	if err != nil {
		return Release{}, false, err
	}
	var gr ghRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return Release{}, false, fmt.Errorf("parse release: %w", err)
	}
	latest, err := parseVersion(gr.TagName)
	if err != nil {
		return Release{}, false, fmt.Errorf("latest release: %w", err)
	}
	cur, _ := parseVersion(current) // Checkable guarantees it parses
	rel := Release{
		Tag:        gr.TagName,
		Version:    strings.TrimPrefix(gr.TagName, "v"),
		Prerelease: gr.Prerelease || latest.prerelease(),
		Assets:     gr.Assets,
	}
	if rel.Prerelease && !cur.prerelease() {
		return rel, false, nil
	}
	return rel, latest.compare(cur) > 0, nil
}

// Check uses a default Client.
func Check(ctx context.Context, current string) (Release, bool, error) {
	return (&Client{}).Check(ctx, current)
}

// ---- throttle state ----

// State is persisted between launches to throttle automatic checks.
type State struct {
	LastCheck time.Time `json:"last_check"`
	Latest    string    `json:"latest,omitempty"`
}

// Due reports whether an automatic check should run now. A clock that moved
// backwards does not trigger a check.
func (s State) Due(now time.Time) bool {
	if s.LastCheck.IsZero() {
		return true
	}
	return now.Sub(s.LastCheck) >= CheckInterval
}

// DefaultStatePath is ~/.orchestra/update.json.
func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orchestra", "update.json"), nil
}

// LoadState reads the state file; a missing or corrupt file yields the zero
// State (i.e. a check is due).
func LoadState(path string) State {
	var s State
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	if json.Unmarshal(b, &s) != nil {
		return State{}
	}
	return s
}

// SaveState writes the state file, creating its directory.
func SaveState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNotWritable is returned (wrapped) when the executable's directory cannot
// be written, e.g. a system-wide install.
var ErrNotWritable = errors.New("install directory is not writable")
