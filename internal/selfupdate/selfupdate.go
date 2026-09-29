// Package selfupdate lets users upgrade the whis binary in place from
// GitHub Releases with `whis update`. No key or auth needed: the repo is
// public, so the latest-release API and asset downloads are anonymous.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const repo = "NamanGaonkar/WHIS---CLi-coder"

// Latest fetches the newest published release tag ("v0.2.10").
func Latest() (string, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var out struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.TagName == "" {
		return "", fmt.Errorf("no published release found")
	}
	return out.TagName, nil
}

// Run upgrades the running binary to the latest release and reports what it
// did. current is the compiled-in version string.
func Run(current string) error {
	fmt.Printf("checking for updates (current: %s)...\n", current)
	latest, err := Latest()
	if err != nil {
		return fmt.Errorf("cannot reach GitHub releases: %w", err)
	}
	if !newer(current, latest) {
		// Same version can still be a NEW build: when a release is re-published
		// under the same tag (e.g. perf fixes shipped as v0.2.23 again), the
		// semver compare sees no bump. Detect it by comparing the running
		// binary's checksum with the release's published .sha256. Only the
		// exact current version is consulted so a locally-built dev binary
		// with a bogus version string never triggers spurious downloads.
		if norm(current) == norm(latest) {
			if needs, err := releaseRebuilt(current); err == nil && needs {
				fmt.Printf("%s was re-published with fixes — updating...\n", latest)
			} else {
				fmt.Printf("whis is up to date (%s).\n", current)
				return nil
			}
		} else {
			fmt.Printf("whis is up to date (%s).\n", current)
			return nil
		}
	}
	fmt.Printf("updating %s -> %s ...\n", current, latest)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, lerr := filepath.EvalSymlinks(exe); lerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	// Download the new binary NEXT TO the target so the final rename stays
	// on the same volume (Windows cannot rename across drives).
	asset := assetName()
	tmp, err := os.CreateTemp(dir, ".whis-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away
	if err := download(assetURL(asset), tmp); err != nil {
		tmp.Close()
		return fmt.Errorf("download failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Release workflow always ships a .sha256 beside every asset; verify
	// when present so a truncated download never replaces a working whis.
	if sum, serr := fetchText(assetURL(asset + ".sha256")); serr == nil {
		if err := verifyChecksum(tmpName, strings.Fields(sum)[0]); err != nil {
			return err
		}
		fmt.Println("checksum ok")
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	// A running exe cannot be overwritten on Windows, but it CAN be renamed:
	// move ours aside, slide the new one in, roll back on any failure.
	old := exe + ".old"
	_ = os.Remove(old) // leftover from a previous update
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("could not move the running binary aside (%w) — close other whis windows and retry", err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("install failed: %w", err)
	}
	// Deleting the just-renamed running exe fails on Windows; leave it for
	// the next update to sweep (best effort).
	_ = os.Remove(old)
	fmt.Printf("updated whis -> %s.\nStart a new whis window to use it.\n", latest)
	return nil
}

func assetURL(asset string) string {
	return "https://github.com/" + repo + "/releases/latest/download/" + asset
}

// assetName mirrors the release workflow's dist/ naming.
func assetName() string {
	name := "whis-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func norm(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// newer reports whether latest is strictly newer than current. Dev suffixes
// ("v0.2.11-test") count as their base version, so a dev build never
// "updates" itself down to an older published release.
// releaseRebuilt reports whether the published checksum for the current
// version's asset differs from the running binary (i.e. the release was
// rebuilt/re-published under the same tag). A checksum fetch failure or an
// unknown current version returns an error: callers treat that as
// "up to date" — never force a download on a maybe.
func releaseRebuilt(current string) (bool, error) {
	if v := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(current)), "v"); v == "" || v[0] < '0' || v[0] > '9' {
		return false, fmt.Errorf("non-release version %q", current)
	}
	sum, err := fetchText(assetURL(assetName() + ".sha256"))
	if err != nil {
		return false, err
	}
	want := strings.Fields(sum)
	if len(want) == 0 {
		return false, fmt.Errorf("empty checksum file")
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	if resolved, lerr := filepath.EvalSymlinks(exe); lerr == nil {
		exe = resolved
	}
	f, err := os.Open(exe)
	if err != nil {
		return false, err
	}
	defer f.Close()
	got := sha256.New()
	if _, err := io.Copy(got, f); err != nil {
		return false, err
	}
	return !strings.EqualFold(hex.EncodeToString(got.Sum(nil)), strings.TrimSpace(want[0])), nil
}

func newer(current, latest string) bool {
	c, l := verNums(current), verNums(latest)
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// verNums extracts the first three numeric components of a version string.
func verNums(v string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(norm(v), ".", 3) {
		if i >= 3 {
			break
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out
}

func download(url string, f *os.File) error {
	c := &http.Client{} // no overall timeout: big binary, slow links
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s", resp.Status)
	}
	_, err = io.Copy(f, resp.Body)
	return err
}

func fetchText(url string) (string, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

func verifyChecksum(path, want string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	got := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(got[:]), strings.TrimSpace(want)) {
		return fmt.Errorf("checksum mismatch — aborting, your whis was NOT changed")
	}
	return nil
}
