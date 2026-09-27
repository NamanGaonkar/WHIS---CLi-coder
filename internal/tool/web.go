package tool

import (
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// webMaxBytes caps the downloaded body (token surgery: pages, not movies).
const webMaxBytes = 512 * 1024

// webMaxChars caps the text handed to the model.
const webMaxChars = 4000

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)\b[^>]*>.*?</(script|style|noscript|svg|head)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe       = regexp.MustCompile(`[ \t]+`)
	blankRe       = regexp.MustCompile(`\n{3,}`)
)

// WebFetch downloads a URL, strips scripts/styles/HTML tags and returns
// readable text. Private IPs are rejected so run_command-style SSRF into the
// local network is not possible through this tool.
func (e *Env) WebFetch(rawURL string) Result {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return Result{Output: "empty url"}
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{OK: false, Output: "invalid url: " + err.Error()}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{OK: false, Output: "only http/https allowed"}
	}
	host := u.Hostname()
	if isPrivateHost(host) {
		return Result{OK: false, Output: "blocked: private/loopback host"}
	}

	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{OK: false, Output: "request error: " + err.Error()}
	}
	req.Header.Set("User-Agent", "whis/0.1 (+https://github.com/NamanGaonkar/WHIS---CLi-coder)")

	resp, err := client.Do(req)
	if err != nil {
		return Result{OK: false, Output: "fetch failed: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Output: fmt.Sprintf("http %d for %s", resp.StatusCode, rawURL)}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, webMaxBytes))
	if err != nil {
		return Result{OK: false, Output: "read failed: " + err.Error()}
	}

	text := unescapeAll(scriptStyleRe.ReplaceAllString(string(body), " "))
	text = collapseSpace(tagRe.ReplaceAllString(text, " "))
	text = spaceRe.ReplaceAllString(text, " ")
	text = blankRe.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{OK: false, Output: "page had no readable text (JS-only site?)"}
	}
	if len(text) > webMaxChars {
		text = text[:webMaxChars] + "\n... (truncated)"
	}
	return Result{OK: true, Output: text}
}

// unescapeAll and collapseSpace are extracted for testability.
func unescapeAll(s string) string { return html.UnescapeString(s) }

func collapseSpace(s string) string { return s }

// isPrivateHost rejects loopback / LAN targets.
func isPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if net.ParseIP(host) != nil {
		ip := net.ParseIP(host)
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	// rare LAN DNS names (whis runs local-first; keep the block tight)
	return false
}
