package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNorm(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"v0.2.10", "0.2.10"}, {"0.2.10", "0.2.10"}, {" v0.2.9 ", "0.2.9"},
	} {
		if got := norm(c.in); got != c.want {
			t.Errorf("norm(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cur, lat string
		want     bool
	}{{"v0.2.9", "v0.2.10", true}, {"v0.2.10", "v0.2.10", false},
		{"v0.2.11", "v0.2.10", false}, {"v0.2.11-test", "v0.2.10", false},
		{"v0.2.9", "v0.3.0", true}, {"v1.0.0", "v0.9.9", false}, {"0.2.9", "0.10.0", true}} {
		if got := newer(c.cur, c.lat); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.cur, c.lat, got, c.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	n := assetName()
	if !strings.HasPrefix(n, "whis-") || !strings.Contains(n, "-amd64") || !strings.Contains(n, "-arm") && !strings.Contains(n, "amd") {
		t.Fatalf("unexpected asset name %q", n)
	}
	if isWindows() && !strings.HasSuffix(n, ".exe") {
		t.Errorf("windows asset must end in .exe, got %q", n)
	}
}

func isWindows() bool { return filepath.Separator == '\\' }

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sha256("hello")
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := verifyChecksum(p, want); err != nil {
		t.Fatalf("good checksum rejected: %v", err)
	}
	if err := verifyChecksum(p, strings.ToUpper(want)); err != nil {
		t.Fatalf("case-insensitive checksum failed: %v", err)
	}
	if err := verifyChecksum(p, "deadbeef"); err == nil {
		t.Fatal("bad checksum accepted")
	}
}
