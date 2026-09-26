package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// AssetName is the archive GoReleaser publishes for a platform. It must stay
// in sync with archives.name_template in .goreleaser.yaml.
func AssetName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("orchestra_%s_%s_%s.%s", version, goos, goarch, ext)
}

const checksumsName = "checksums.txt"

// Install downloads rel for this platform, verifies it against checksums.txt,
// and atomically replaces the executable at exePath (symlinks are resolved, so
// the real file is replaced and the link is left alone). On any error the
// existing executable is left untouched.
func (c *Client) Install(ctx context.Context, rel Release, exePath string) error {
	goos, goarch := c.goos(), c.goarch()
	name := AssetName(rel.Version, goos, goarch)
	archive, ok := findAsset(rel.Assets, name)
	if !ok {
		return fmt.Errorf("release %s has no build for %s/%s (expected %s)", rel.Tag, goos, goarch, name)
	}
	sums, ok := findAsset(rel.Assets, checksumsName)
	if !ok {
		return fmt.Errorf("release %s has no %s; refusing to install an unverified binary", rel.Tag, checksumsName)
	}

	target, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if err := checkWritable(filepath.Dir(target)); err != nil {
		return err
	}

	sumBody, err := c.get(ctx, sums.URL, "", maxMetadata)
	if err != nil {
		return fmt.Errorf("download %s: %w", checksumsName, err)
	}
	want, ok := parseChecksums(sumBody)[name]
	if !ok {
		return fmt.Errorf("%s has no checksum entry for %s; refusing to install", checksumsName, name)
	}

	data, err := c.get(ctx, archive.URL, "application/octet-stream", c.maxDownload())
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	got := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return fmt.Errorf("checksum mismatch for %s (got %x, want %s); refusing to install", name, got, want)
	}

	binName := "orchestra"
	if goos == "windows" {
		binName = "orchestra.exe"
	}
	var bin []byte
	if strings.HasSuffix(name, ".zip") {
		bin, err = extractZip(data, binName, c.maxDownload())
	} else {
		bin, err = extractTarGz(data, binName, c.maxDownload())
	}
	if err != nil {
		return err
	}
	return replaceExecutable(target, bin, goos == "windows")
}

// Install uses a default Client.
func Install(ctx context.Context, rel Release, exePath string) error {
	return (&Client{}).Install(ctx, rel, exePath)
}

func findAsset(assets []Asset, name string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// parseChecksums reads GoReleaser's "<sha256>  <filename>" lines.
func parseChecksums(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return out
}

// entryMatches validates an archive entry name and reports whether it is the
// binary: either at the archive root or one directory deep. Any absolute or
// parent-escaping entry makes the whole archive unsafe.
func entryMatches(name, binName string) (bool, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || filepath.IsAbs(name) || (len(n) > 1 && n[1] == ':') {
		return false, fmt.Errorf("archive contains unsafe path %q", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return false, fmt.Errorf("archive contains unsafe path %q", name)
		}
	}
	clean := path.Clean(n)
	if path.Base(clean) != binName {
		return false, nil
	}
	return strings.Count(clean, "/") <= 1, nil
}

func readCapped(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("binary in archive exceeds the %d byte limit", limit)
	}
	return b, nil
}

func extractTarGz(data []byte, binName string, limit int64) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var bin []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		ok, err := entryMatches(hdr.Name, binName)
		if err != nil {
			return nil, err
		}
		if !ok || hdr.Typeflag != tar.TypeReg || bin != nil {
			continue
		}
		if bin, err = readCapped(tr, limit); err != nil {
			return nil, err
		}
	}
	if len(bin) == 0 {
		return nil, fmt.Errorf("archive does not contain %s", binName)
	}
	return bin, nil
}

func extractZip(data []byte, binName string, limit int64) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	var bin []byte
	for _, f := range zr.File {
		ok, err := entryMatches(f.Name, binName)
		if err != nil {
			return nil, err
		}
		if !ok || !f.Mode().IsRegular() || bin != nil {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		bin, err = readCapped(rc, limit)
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(bin) == 0 {
		return nil, fmt.Errorf("archive does not contain %s", binName)
	}
	return bin, nil
}

func notWritable(dir string, err error) error {
	return fmt.Errorf("%w: cannot write to %s (%v) — re-run `orchestra update` as a user who can write there, or reinstall the new release the way you installed Orchestra (e.g. brew/scoop)", ErrNotWritable, dir, err)
}

// checkWritable probes dir before downloading anything, so a system-wide
// install fails fast with a clear hint.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".orchestra-probe-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return notWritable(dir, err)
		}
		return fmt.Errorf("prepare update in %s: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// replaceExecutable writes bin next to target and swaps it in. On unix a
// rename over the running binary is atomic and safe. Windows cannot overwrite
// a running .exe but can rename it, so the old one is moved to <exe>.old
// (removed by CleanupOld on the next launch) before the new one is moved in.
func replaceExecutable(target string, bin []byte, windows bool) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".orchestra-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return notWritable(dir, err)
		}
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return fmt.Errorf("write update: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write update: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod update: %w", err)
	}

	if !windows {
		if err := os.Rename(tmpName, target); err != nil {
			return fmt.Errorf("replace %s: %w", target, err)
		}
		ok = true
		return nil
	}

	old := target + ".old"
	_ = os.Remove(old) // stale leftover from a previous update
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("move current executable aside: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Rename(old, target) // roll back
		return fmt.Errorf("replace %s: %w", target, err)
	}
	ok = true
	return nil
}

// CleanupOld best-effort removes the <exe>.old left behind by a Windows
// update. Safe to call on every launch and on every platform.
func CleanupOld(exePath string) {
	if exePath == "" {
		return
	}
	if target, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = target
	}
	_ = os.Remove(exePath + ".old")
}
