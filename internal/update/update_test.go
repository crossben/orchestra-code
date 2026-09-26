package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"0.10.0", "0.9.0", 1},
		{"0.9.0", "0.10.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0-rc2", "1.0.0-rc1", 1},
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
		{"1.0", "1.0.0", 0},
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil {
			t.Fatalf("Compare(%q,%q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"", "dev", "x.y.z", "1", "1.2.3.4", "1.-2.3"} {
		if _, err := Compare(bad, "1.0.0"); err == nil {
			t.Errorf("Compare(%q) should fail", bad)
		}
	}
}

func TestCheckable(t *testing.T) {
	for v, want := range map[string]bool{
		"0.8.0":        true,
		"v0.8.0":       true,
		"0.9.0-rc1":    true,
		"dev":          false,
		"":             false,
		"garbage":      false,
		"0.8.1-next":   false,
		"0.8.1-next.3": false,
	} {
		if got := Checkable(v); got != want {
			t.Errorf("Checkable(%q) = %v, want %v", v, got, want)
		}
	}
}

// releaseServer serves /repos/<repo>/releases/latest with the given JSON body.
func releaseServer(t *testing.T, status int, body string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.URL.Path != "/repos/"+DefaultRepo+"/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck(t *testing.T) {
	body := `{"tag_name":"v0.9.0","prerelease":false,"assets":[{"name":"checksums.txt","browser_download_url":"http://x/checksums.txt","size":10}]}`
	srv := releaseServer(t, 200, body, nil)
	c := &Client{APIBase: srv.URL}

	rel, newer, err := c.Check(context.Background(), "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	if !newer || rel.Version != "0.9.0" || rel.Tag != "v0.9.0" || len(rel.Assets) != 1 || rel.Assets[0].URL != "http://x/checksums.txt" {
		t.Fatalf("unexpected: newer=%v rel=%+v", newer, rel)
	}
	for _, cur := range []string{"0.9.0", "v0.9.0", "1.0.0"} {
		if _, newer, err := c.Check(context.Background(), cur); err != nil || newer {
			t.Errorf("current %s: newer=%v err=%v, want false/nil", cur, newer, err)
		}
	}
	// A prerelease of the same version is upgraded to the stable.
	if _, newer, _ := c.Check(context.Background(), "0.9.0-rc1"); !newer {
		t.Error("0.9.0-rc1 should be offered 0.9.0")
	}
}

func TestCheckPrereleaseNeverOfferedOverStable(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v1.0.0-rc1","prerelease":true,"assets":[]}`,
		`{"tag_name":"v1.0.0-rc1","prerelease":false,"assets":[]}`,
		`{"tag_name":"v1.0.0","prerelease":true,"assets":[]}`,
	} {
		c := &Client{APIBase: releaseServer(t, 200, body, nil).URL}
		if _, newer, err := c.Check(context.Background(), "0.8.0"); err != nil || newer {
			t.Errorf("%s: newer=%v err=%v, want false/nil", body, newer, err)
		}
	}
	// ...but a prerelease build may move to a newer prerelease.
	c := &Client{APIBase: releaseServer(t, 200, `{"tag_name":"v1.0.0-rc2","prerelease":true}`, nil).URL}
	if _, newer, _ := c.Check(context.Background(), "1.0.0-rc1"); !newer {
		t.Error("1.0.0-rc1 should be offered 1.0.0-rc2")
	}
}

func TestCheckSkipsUncheckableWithoutNetwork(t *testing.T) {
	var hits int32
	c := &Client{APIBase: releaseServer(t, 200, `{"tag_name":"v9.9.9"}`, &hits).URL}
	for _, cur := range []string{"dev", "", "0.8.1-next", "nonsense"} {
		if _, newer, err := c.Check(context.Background(), cur); err != nil || newer {
			t.Errorf("%q: newer=%v err=%v", cur, newer, err)
		}
	}
	if hits != 0 {
		t.Fatalf("server was hit %d times for uncheckable versions", hits)
	}
}

func TestCheckErrors(t *testing.T) {
	c := &Client{APIBase: releaseServer(t, 403, `rate limited`, nil).URL}
	if _, _, err := c.Check(context.Background(), "0.8.0"); err == nil {
		t.Error("non-200 should error")
	}
	c = &Client{APIBase: releaseServer(t, 200, `not json`, nil).URL}
	if _, _, err := c.Check(context.Background(), "0.8.0"); err == nil {
		t.Error("bad JSON should error")
	}
	c = &Client{APIBase: releaseServer(t, 200, `{"tag_name":"latest"}`, nil).URL}
	if _, _, err := c.Check(context.Background(), "0.8.0"); err == nil {
		t.Error("unparseable tag should error")
	}
}

func TestStateThrottle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "update.json")
	st := LoadState(path) // missing → zero
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if !st.Due(now) {
		t.Fatal("zero state should be due")
	}
	st.LastCheck = now
	st.Latest = "0.9.0"
	if err := SaveState(path, st); err != nil {
		t.Fatal(err)
	}
	got := LoadState(path)
	if !got.LastCheck.Equal(now) || got.Latest != "0.9.0" {
		t.Fatalf("round trip: %+v", got)
	}
	if got.Due(now.Add(23 * time.Hour)) {
		t.Error("should not be due after 23h")
	}
	if !got.Due(now.Add(24 * time.Hour)) {
		t.Error("should be due after 24h")
	}
	if got.Due(now.Add(-time.Hour)) {
		t.Error("clock skew backwards should not trigger a check")
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !LoadState(path).Due(now) {
		t.Error("corrupt state should be treated as due")
	}
}

// ---- Install ----

type entry struct {
	name string
	body string
	dir  bool
}

func makeTarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			hdr = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fixture serves an archive and a checksums file and returns a matching Release.
func fixture(t *testing.T, version, goos, goarch string, archive []byte, checksums string) Release {
	t.Helper()
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	name := fmt.Sprintf("orchestra_%s_%s_%s.%s", version, goos, goarch, ext)
	if checksums == "" {
		checksums = fmt.Sprintf("%s  %s\n%s  orchestra_%s_other_os.tar.gz\n", sha(archive), name, strings.Repeat("0", 64), version)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(checksums)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Release{
		Tag:     "v" + version,
		Version: version,
		Assets: []Asset{
			{Name: name, URL: srv.URL + "/dl/" + name, Size: int64(len(archive))},
			{Name: "checksums.txt", URL: srv.URL + "/dl/checksums.txt"},
		},
	}
}

func fakeExe(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s = %q, want %q", path, b, want)
	}
}

func TestInstallTarGz(t *testing.T) {
	arc := makeTarGz(t, []entry{
		{name: "LICENSE", body: "mit"},
		{name: "README.md", body: "readme"},
		{name: "orchestra", body: "NEW-BINARY"},
	})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	exe := fakeExe(t, "orchestra")
	c := &Client{GOOS: "linux", GOARCH: "amd64"}
	if err := c.Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	assertFile(t, exe, "NEW-BINARY")
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(exe)
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
		}
	}
	// No leftover temp files next to the exe.
	ents, _ := os.ReadDir(filepath.Dir(exe))
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestInstallTarGzNestedDir(t *testing.T) {
	arc := makeTarGz(t, []entry{
		{name: "orchestra_0.9.0/", dir: true},
		{name: "orchestra_0.9.0/orchestra", body: "NESTED"},
	})
	rel := fixture(t, "0.9.0", "darwin", "arm64", arc, "")
	exe := fakeExe(t, "orchestra")
	c := &Client{GOOS: "darwin", GOARCH: "arm64"}
	if err := c.Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	assertFile(t, exe, "NESTED")
}

func TestInstallFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	arc := makeTarGz(t, []entry{{name: "orchestra", body: "NEW"}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	real := fakeExe(t, "orchestra")
	link := filepath.Join(t.TempDir(), "orchestra-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c := &Client{GOOS: "linux", GOARCH: "amd64"}
	if err := c.Install(context.Background(), rel, link); err != nil {
		t.Fatal(err)
	}
	assertFile(t, real, "NEW")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink should be left in place")
	}
}

func TestInstallZipWindowsStrategy(t *testing.T) {
	arc := makeZip(t, []entry{
		{name: "LICENSE", body: "mit"},
		{name: "orchestra.exe", body: "NEW-EXE"},
	})
	rel := fixture(t, "0.9.0", "windows", "amd64", arc, "")
	exe := fakeExe(t, "orchestra.exe")
	// A stale .old from a previous update must not block the rename.
	if err := os.WriteFile(exe+".old", []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{GOOS: "windows", GOARCH: "amd64"}
	if err := c.Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	assertFile(t, exe, "NEW-EXE")
	assertFile(t, exe+".old", "OLD")

	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Errorf(".old should be removed, stat err = %v", err)
	}
	CleanupOld(exe) // idempotent
}

func TestInstallChecksumMismatch(t *testing.T) {
	arc := makeTarGz(t, []entry{{name: "orchestra", body: "EVIL"}})
	name := "orchestra_0.9.0_linux_amd64.tar.gz"
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, strings.Repeat("a", 64)+"  "+name+"\n")
	exe := fakeExe(t, "orchestra")
	err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	assertFile(t, exe, "OLD")
}

func TestInstallMissingChecksumEntry(t *testing.T) {
	arc := makeTarGz(t, []entry{{name: "orchestra", body: "NEW"}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, strings.Repeat("a", 64)+"  something_else.tar.gz\n")
	exe := fakeExe(t, "orchestra")
	err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want missing checksum error, got %v", err)
	}
	assertFile(t, exe, "OLD")
}

func TestInstallMissingChecksumsAsset(t *testing.T) {
	arc := makeTarGz(t, []entry{{name: "orchestra", body: "NEW"}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	rel.Assets = rel.Assets[:1]
	exe := fakeExe(t, "orchestra")
	if err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe); err == nil {
		t.Fatal("want error when checksums.txt is missing")
	}
	assertFile(t, exe, "OLD")
}

func TestInstallMissingPlatformAsset(t *testing.T) {
	arc := makeZip(t, []entry{{name: "orchestra.exe", body: "NEW"}})
	rel := fixture(t, "0.9.0", "windows", "amd64", arc, "")
	exe := fakeExe(t, "orchestra.exe")
	err := (&Client{GOOS: "windows", GOARCH: "arm64"}).Install(context.Background(), rel, exe)
	if err == nil || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("want missing-asset error naming the platform, got %v", err)
	}
}

func TestInstallRejectsTraversal(t *testing.T) {
	for name, arc := range map[string][]byte{
		"tar dotdot":   makeTarGz(t, []entry{{name: "../orchestra", body: "EVIL"}, {name: "orchestra", body: "NEW"}}),
		"tar absolute": makeTarGz(t, []entry{{name: "/tmp/orchestra", body: "EVIL"}}),
	} {
		rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
		exe := fakeExe(t, "orchestra")
		err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe)
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Errorf("%s: want unsafe-path error, got %v", name, err)
		}
		assertFile(t, exe, "OLD")
	}
	zarc := makeZip(t, []entry{{name: "..\\..\\orchestra.exe", body: "EVIL"}})
	rel := fixture(t, "0.9.0", "windows", "amd64", zarc, "")
	exe := fakeExe(t, "orchestra.exe")
	if err := (&Client{GOOS: "windows", GOARCH: "amd64"}).Install(context.Background(), rel, exe); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Errorf("zip traversal: want unsafe-path error, got %v", err)
	}
}

func TestInstallNoBinaryInArchive(t *testing.T) {
	arc := makeTarGz(t, []entry{{name: "README.md", body: "x"}, {name: "deep/er/orchestra", body: "too deep"}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	exe := fakeExe(t, "orchestra")
	if err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe); err == nil {
		t.Fatal("want error when the archive has no orchestra binary")
	}
	assertFile(t, exe, "OLD")
}

func TestInstallUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced here")
	}
	arc := makeTarGz(t, []entry{{name: "orchestra", body: "NEW"}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	exe := fakeExe(t, "orchestra")
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	err := (&Client{GOOS: "linux", GOARCH: "amd64"}).Install(context.Background(), rel, exe)
	if !errors.Is(err, ErrNotWritable) || !strings.Contains(err.Error(), "reinstall") {
		t.Fatalf("want ErrNotWritable with a hint, got %v", err)
	}
	assertFile(t, exe, "OLD")
}

func TestInstallSizeLimit(t *testing.T) {
	arc := makeTarGz(t, []entry{{name: "orchestra", body: strings.Repeat("x", 4096)}})
	rel := fixture(t, "0.9.0", "linux", "amd64", arc, "")
	exe := fakeExe(t, "orchestra")
	c := &Client{GOOS: "linux", GOARCH: "amd64", MaxDownload: 64}
	if err := c.Install(context.Background(), rel, exe); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("want size-limit error, got %v", err)
	}
}

// Guard: the JSON field names match GitHub's API.
func TestReleaseJSONShape(t *testing.T) {
	var r ghRelease
	if err := json.Unmarshal([]byte(`{"tag_name":"v1.2.3","prerelease":true,"assets":[{"name":"a","browser_download_url":"u","size":3}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.TagName != "v1.2.3" || !r.Prerelease || r.Assets[0].URL != "u" || r.Assets[0].Size != 3 {
		t.Fatalf("%+v", r)
	}
}
