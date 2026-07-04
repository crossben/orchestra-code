# Releasing Orchestra

Releases are automated with [GoReleaser](https://goreleaser.com) via GitHub Actions. Cutting a
release is a single tag push; the pipeline cross-builds every platform, packages archives, generates
checksums, publishes a GitHub Release, and (optionally) updates the Homebrew tap and Scoop bucket.

## Cut a release

```sh
# make sure main is green and up to date, then:
git tag v0.8.0
git push origin v0.8.0
```

Pushing a `v*` tag triggers [.github/workflows/release.yml](../.github/workflows/release.yml), which runs
GoReleaser. In ~2 minutes the [Releases page](https://github.com/crossben/orchestra-code/releases) will have:

- `orchestra_<version>_<os>_<arch>.tar.gz` for linux/darwin (amd64 + arm64)
- `orchestra_<version>_windows_amd64.zip`
- `checksums.txt`
- auto-generated release notes

The version is stamped into the binary at build time via `-ldflags "-X main.version=<tag>"`, so
`orchestra --version` reports the tag.

**Versioning:** semver, `vMAJOR.MINOR.PATCH`. Pre-release suffixes (e.g. `v0.8.0-rc1`) are published as
GitHub pre-releases automatically (`prerelease: auto`).

## One-time setup: Homebrew tap + Scoop bucket

These let users `brew install` / `scoop install`. They're **optional** — without the token below, the
release still succeeds and just skips them. Requires two (already created) repos:

- `crossben/homebrew-tap`
- `crossben/scoop-bucket`

GoReleaser pushes generated manifests into those repos, which needs a token with write access to them
(the built-in `GITHUB_TOKEN` only has access to `orchestra-code`, not other repos).

### 1. Create a Personal Access Token

**Fine-grained (recommended — least privilege):**
1. https://github.com/settings/tokens?type=beta → **Generate new token**
2. **Resource owner:** `crossben`
3. **Repository access:** *Only select repositories* → pick **`homebrew-tap`** and **`scoop-bucket`**
4. **Permissions → Repository permissions → Contents:** *Read and write*
   (Metadata → Read-only is added automatically)
5. Generate and copy the token.

**Classic (simpler):**
1. https://github.com/settings/tokens → **Generate new token (classic)**
2. Scope: **`repo`**
3. Generate and copy the token.

### 2. Add it as a repository secret

In **`orchestra-code`** → **Settings → Secrets and variables → Actions → New repository secret**:

- **Name:** `TAP_GITHUB_TOKEN`
- **Value:** the token from step 1

That's it. The next `v*` tag will publish the formula to `homebrew-tap/Formula/orchestra.rb` and the
manifest to `scoop-bucket/orchestra.json`. Users then install with:

```sh
brew install crossben/tap/orchestra
# or
scoop bucket add orchestra https://github.com/crossben/scoop-bucket
scoop install orchestra
```

## Repo social preview (one-time)

**Settings → General → Social preview → Edit → Upload** [`assets/social-preview.png`](../assets/social-preview.png)
(1280×640). GitHub requires a raster image here, so the SVG logo can't be used directly.

## Local dry run (optional)

To preview what a release would build without publishing (requires the `goreleaser` binary):

```sh
goreleaser release --snapshot --clean   # builds into ./dist, no upload
goreleaser check                          # validate .goreleaser.yaml
```

CI already runs `goreleaser check` on every push (the `release-config` job in
[ci.yml](../.github/workflows/ci.yml)), so config errors surface before you tag.
