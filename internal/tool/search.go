package tool

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// skipDirs are pruned from search and tree walks.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, "vendor": true, "dist": true, "build": true,
	".idea": true, ".vscode": true, "target": true,
}

// maxSearchResults caps search output (token surgery).
const maxSearchResults = 60

// SearchCodebase implements search_codebase: ripgrep if available on PATH,
// otherwise a pure-Go regex walker. Output is file:line: match capped hard.
func (e *Env) SearchCodebase(pattern, glob string) Result {
	if strings.TrimSpace(pattern) == "" {
		return Result{Output: "empty pattern"}
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return Result{Output: "invalid regex: " + err.Error()}
	}
	if rg, err := exec.LookPath("rg"); err == nil {
		args := []string{"-n", "--no-heading", "-m", "5", "--glob", "!.git", "--glob", "!node_modules"}
		if glob != "" {
			args = append(args, "--glob", glob)
		}
		args = append(args, pattern, ".")
		cmd := exec.Command(rg, args...)
		cmd.Dir = e.Root
		out, err := cmd.Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) > maxSearchResults {
				lines = append(lines[:maxSearchResults], fmt.Sprintf("... (%d more matches truncated)", len(lines)-maxSearchResults))
			}
			if len(lines) == 1 && lines[0] == "" {
				return Result{OK: true, Output: "no matches"}
			}
			return Result{OK: true, Output: strings.Join(lines, "\n")}
		}
		// fall through to pure-Go on any rg failure
	}
	return Result{OK: true, Output: e.searchGo(pattern, glob)}
}

// searchGo is the pure-Go fallback searcher (always available).
func (e *Env) searchGo(pattern, glob string) string {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "invalid regex: " + err.Error()
	}
	globRe := globToRegexp(glob)
	var out []string
	count := 0
	_ = filepath.WalkDir(e.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipDirs[name] || (strings.HasPrefix(name, ".") && path != e.Root) {
				if d.IsDir() && path != e.Root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if globRe != nil && !globRe.MatchString(name) {
			return nil
		}
		if isBinaryName(name) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		hits := 0
		for sc.Scan() {
			ln++
			if hits >= 5 {
				break
			}
			if re.Match(sc.Bytes()) {
				hits++
				line := sc.Text()
				if len(line) > 240 {
					line = line[:240] + "…"
				}
				out = append(out, e.rel(path)+":"+fmt.Sprint(ln)+": "+strings.TrimSpace(line))
				count++
				if count >= maxSearchResults {
					out = append(out, "... (more matches truncated)")
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if len(out) == 0 {
		return "no matches"
	}
	return strings.Join(out, "\n")
}

// globToRegexp converts a simple *.go style glob to a regexp (nil if empty).
func globToRegexp(glob string) *regexp.Regexp {
	if glob == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		case '.':
			b.WriteString("\\.")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// isBinaryName filters obvious binary extensions from walks.
func isBinaryName(name string) bool {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".ico", ".exe", ".dll", ".so", ".dylib", ".zip", ".gz", ".tar", ".pdf", ".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4", ".mov", ".avi", ".class", ".jar", ".bin", ".db", ".sqlite", ".lock"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// ListTree implements list_tree: workspace tree to depth 4, pruned.
func (e *Env) ListTree(start string) Result {
	root := e.Root
	if start != "" {
		abs, err := e.resolve(start)
		if err != nil {
			return Result{Output: err.Error()}
		}
		root = abs
	}
	var out []string
	count := 0
	var walk func(dir string, prefix string, depth int)
	walk = func(dir string, prefix string, depth int) {
		if depth > 4 || count > 400 {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(ents, func(i, j int) bool {
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return ents[i].Name() < ents[j].Name()
		})
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() {
				if skipDirs[name] || strings.HasPrefix(name, ".") {
					continue
				}
				out = append(out, prefix+name+"/")
				count++
				walk(filepath.Join(dir, name), prefix+"  ", depth+1)
			} else {
				if isBinaryName(name) {
					continue
				}
				out = append(out, prefix+name)
				count++
			}
			if count > 400 {
				out = append(out, "... (tree truncated)")
				return
			}
		}
	}
	walk(root, "", 1)
	if len(out) == 0 {
		return Result{OK: true, Output: "(empty)"}
	}
	return Result{OK: true, Output: strings.Join(out, "\n")}
}

// ensure imports used
var (
	_ = bytes.MinRead
	_ = context.Background
	_ = io.EOF
	_ = time.Second
	_ = exec.Command
	_ = bufio.NewScanner
)
