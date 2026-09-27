package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"

	"whis/internal/config"
)

// glob removes unsupported ** patterns by delegating to filepath.Glob.
var _ = filepath.Glob

// browserMaxText caps page text handed to the model (token surgery).
const browserMaxText = 8000

// browserState holds the lazily-launched Playwright browser. One instance
// stays alive across every tool call in the process; it is closed when the
// TUI/headless session ends (CloseBrowser is registered via OnExit).
type browserState struct {
	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser
	page    playwright.Page
	err     error // launch failure memo: never retry a broken install
}

var brow = &browserState{}

// BrowserInstalled reports whether the Playwright Chromium is on disk, so
// whis can suggest `whis browser-install` before the model even tries.
func BrowserInstalled() bool {
	_, err := os.Stat(chromiumMarker())
	return err == nil
}

// chromiumMarker finds any installed Chromium build under the Playwright
// browsers dir (version-agnostic: matches chromium-<rev> or
// chromium_headless_shell-<rev> on any platform layout).
func chromiumMarker() string {
	base := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if base == "" {
		if x := os.Getenv("LOCALAPPDATA"); x != "" && isWindows() {
			base = filepath.Join(x, "ms-playwright")
		} else {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".cache", "ms-playwright")
		}
	}
	for _, pattern := range []string{
		filepath.Join(base, "chromium-*", "chrome-win*", "chrome.exe"),
		filepath.Join(base, "chromium-*", "chrome-win*", "headless_shell.exe"),
		filepath.Join(base, "chromium_headless_shell-*", "chrome-win*", "headless_shell.exe"),
		filepath.Join(base, "chromium-*", "chrome-linux", "chrome"),
		filepath.Join(base, "chromium-*", "chrome-mac", "Chromium.app"),
	} {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

func isWindows() bool { return os.PathSeparator == '\\' }

// launch spins up the browser once. Errors are memoized so a broken install
// returns instantly instead of timing out on every call.
func (b *browserState) launch() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if b.browser != nil {
		return nil
	}
	if !BrowserInstalled() {
		b.err = fmt.Errorf("browser engine not installed — run: whis browser-install (one-time Chromium download), or use web_search/web_fetch instead")
		return b.err
	}
	pw, err := playwright.Run()
	if err != nil {
		b.err = fmt.Errorf("playwright driver failed: %w — run: whis browser-install", err)
		return b.err
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true)})
	if err != nil {
		_ = pw.Stop()
		b.err = fmt.Errorf("chromium launch failed: %w — run: whis browser-install", err)
		return b.err
	}
	b.pw = pw
	b.browser = browser
	return nil
}

// page returns the persistent page, re-creating it if closed.
func (b *browserState) page1() (playwright.Page, error) {
	if err := b.launch(); err != nil {
		return nil, b.err
	}
	if b.page != nil {
		if !b.page.IsClosed() {
			return b.page, nil
		}
		b.page = nil
	}
	page, err := b.browser.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page failed: %w", err)
	}
	page.SetDefaultTimeout(15000)
	b.page = page
	return page, nil
}

// visibleText extracts rendered text (JS included) via innerText on body.
func visibleText(page playwright.Page) (string, error) {
	el, err := page.QuerySelector("body")
	if err != nil || el == nil {
		return "", err
	}
	txt, err := el.InnerText()
	if err != nil {
		return "", err
	}
	txt = strings.TrimSpace(squashLines(cleanText(txt)))
	if txt == "" {
		return "", fmt.Errorf("page rendered no text (blank body)")
	}
	if len(txt) > browserMaxText {
		txt = txt[:browserMaxText] + "\n... (truncated)"
	}
	return txt, nil
}

// Browser runs one browser action. actions: navigate | click | get_text |
// screenshot | close. The browser instance persists across calls so the
// model can navigate then interact step by step.
func (e *Env) Browser(action, arg string) Result {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "close" {
		CloseBrowser()
		return Result{OK: true, Output: "browser closed"}
	}
	page, err := brow.page1()
	if err != nil {
		return Result{OK: false, Output: err.Error()}
	}
	switch action {
	case "navigate":
		arg = strings.TrimSpace(arg)
		if arg == "" {
			return Result{OK: false, Output: "navigate needs a url"}
		}
		if !strings.Contains(arg, "://") {
			arg = "https://" + arg
		}
		if isPrivateHost(mustHost(arg)) {
			return Result{OK: false, Output: "blocked: private/loopback host"}
		}
		if _, err := page.Goto(arg, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded, Timeout: playwright.Float(20000)}); err != nil {
			return Result{OK: false, Output: "navigation failed: " + err.Error()}
		}
		// give JS challenges a beat to resolve; passive checks pass here
		time.Sleep(1200 * time.Millisecond)
		title, _ := page.Title()
		txt, err := visibleText(page)
		if err != nil {
			return Result{OK: true, Output: fmt.Sprintf("loaded %s (title: %s) but no readable text: %v", arg, title, err)}
		}
		return Result{OK: true, Output: fmt.Sprintf("loaded %s (title: %s)\n\n%s", arg, title, txt)}
	case "click":
		if strings.TrimSpace(arg) == "" {
			return Result{OK: false, Output: "click needs a css selector"}
		}
		if err := page.Click(arg, playwright.PageClickOptions{Timeout: playwright.Float(8000)}); err != nil {
			return Result{OK: false, Output: "click failed: " + err.Error()}
		}
		time.Sleep(600 * time.Millisecond) // let the page react
		title, _ := page.Title()
		return Result{OK: true, Output: fmt.Sprintf("clicked %s (page now: %s)", arg, title)}
	case "get_text":
		if strings.TrimSpace(arg) == "" {
			return Result{OK: false, Output: "get_text needs a css selector"}
		}
		el, err := page.QuerySelector(arg)
		if err != nil || el == nil {
			return Result{OK: false, Output: "selector not found: " + arg}
		}
		txt, err := el.InnerText()
		if err != nil {
			return Result{OK: false, Output: "text read failed: " + err.Error()}
		}
		txt = strings.TrimSpace(squashLines(cleanText(txt)))
		if len(txt) > browserMaxText {
			txt = txt[:browserMaxText] + "\n... (truncated)"
		}
		return Result{OK: true, Output: txt}
	case "screenshot":
		p := strings.TrimSpace(arg)
		if p == "" {
			p = filepath.Join(config.Dir(), "screenshot.png")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.Root, p)
		}
		if _, err := page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(p), FullPage: playwright.Bool(false)}); err != nil {
			return Result{OK: false, Output: "screenshot failed: " + err.Error()}
		}
		return Result{OK: true, Output: "saved " + p}
	default:
		return Result{OK: false, Output: "unknown browser action " + action + " (navigate | click | get_text | screenshot | close)"}
	}
}

// CloseBrowser tears the browser down (registered on session exit).
func CloseBrowser() {
	brow.mu.Lock()
	defer brow.mu.Unlock()
	if brow.page != nil {
		_ = brow.page.Close()
		brow.page = nil
	}
	if brow.browser != nil {
		_ = brow.browser.Close()
		brow.browser = nil
	}
	if brow.pw != nil {
		_ = brow.pw.Stop()
		brow.pw = nil
	}
	brow.err = nil
}
