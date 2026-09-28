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
	if norm(current) == norm(latest) {
		fmt.Printf("whis is up to date (%s).\n", current)
		return nil
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
