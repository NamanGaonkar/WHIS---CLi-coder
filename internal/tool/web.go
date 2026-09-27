package tool

import (
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// webMaxBytes caps the downloaded body (token surgery: pages, not movies).
const webMaxBytes = 768 * 1024

// webMaxChars caps the text handed to the model.
const webMaxChars = 6000

// webReadPrefix is how much of the extracted text the jina reader may keep.
const webReadPrefix = 6000

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head)\b[^>]*>.*?</(script|style|noscript|svg|head)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe       = regexp.MustCompile(`[ \t]+`)
	blankRe       = regexp.MustCompile(`\n{3,}`)
)

// webClient builds a hardened client: browser-ish TLS/HTTP2 fingerprint,
// accept+language headers, and a cookie jar so Cloudflare's first response
// sets cookies that the retry carries. Timeouts stay tight.
func webClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout: 25 * time.Second,
		Jar:     jar,
	}
}

// webHeaders returns browser-like headers. Cloudflare scores non-browser
// clients largely on TLS fingerprint + header order; a plain Go UA gets
// challenged instantly. Mimic a real Chrome request.
func webHeaders(u *url.URL) http.Header {
	h := http.Header{}
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Accept-Encoding", "gzip, deflate, br")
	h.Set("Cache-Control", "no-cache")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Upgrade-Insecure-Requests", "1")
	h.Set("Referer", u.Scheme+"://"+u.Host+"/")
	return h
}

// blockedPage heuristically detects Cloudflare / captcha / bot-check pages
// so we can fall back to the reader proxy instead of feeding the model a
// wall of challenge text. Strong markers are DOM/JS identifiers that only
// exist inside challenge shells; weak text markers need 2+ hits together so
// legitimate articles mentioning "captcha" are never flagged.
func blockedPage(body, ctype string) bool {
	if strings.Contains(ctype, "text/plain") {
		return false
	}
	low := strings.ToLower(body)
	strong := []string{
		"cdn-cgi/challenge-platform", "challenge-platform", "cf_chl_opt",
		"cf-chl", "cf-browser-verification", "jschl", "___grecaptcha",
		"cf-turnstile", "cf-please-wait",
	}
	for _, m := range strong {
		if strings.Contains(low, m) {
			return true
		}
	}
	weak := []string{
		"checking your browser", "just a moment", "attention required",
		"enable javascript and cookies", "distil networks", "perimeterx",
		"datadome",
	}
	hits := 0
	for _, m := range weak {
		if strings.Contains(low, m) {
			hits++
		}
	}
	return hits >= 2
}

// readerURL maps a target to the r.jina.ai reader (bypasses most bot
// walls because the fetch happens from jina's infrastructure and returns
// clean markdown).
func readerURL(raw string) string {
	return "https://r.jina.ai/" + raw
}

// WebFetch downloads a URL, strips scripts/styles/HTML tags and returns
// readable text. Private IPs are rejected so run_command-style SSRF into the
// local network is not possible through this tool.
//
// Fetch strategy (why this survives Cloudflare / captcha walls):
//  1. browser-identical GET with headers + cookie jar (passive checks pass)
//  2. on challenge pages or http != 200: retry once (cookies now set)
//  3. still blocked: r.jina.ai reader fallback, which returns clean markdown
//     from jina's own crawlers and dodges client-side bot checks entirely
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

	// attempt 1 + 2: direct browser-like fetch with one retry
	for attempt := 0; attempt < 2; attempt++ {
		res := e.tryFetch(rawURL)
		if res.OK {
			return res
		}
		if attempt == 0 {
			time.Sleep(400 * time.Millisecond) // cookies settle before retry
		}
	}

	// attempt 3: reader proxy fallback
	return e.readerFetch(rawURL)
}

// tryFetch performs one direct GET and extracts readable text.
func (e *Env) tryFetch(rawURL string) Result {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{OK: false, Output: "invalid url: " + err.Error()}
	}
	client := webClient()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{OK: false, Output: "request error: " + err.Error()}
	}
	for k, vs := range webHeaders(u) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

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
	ctype := resp.Header.Get("Content-Type")
	if blockedPage(string(body), ctype) {
		return Result{OK: false, Output: "bot challenge page (cloudflare/captcha) — falling back"}
	}

	text := extractText(string(body))
	if text == "" {
		return Result{OK: false, Output: "page had no readable text (JS-only site?)"}
	}
	return Result{OK: true, Output: text}
}

// readerFetch pulls the page through the r.jina.ai reader proxy.
func (e *Env) readerFetch(rawURL string) Result {
	client := webClient()
	req, err := http.NewRequest(http.MethodGet, readerURL(rawURL), nil)
	if err != nil {
		return Result{OK: false, Output: "reader request error: " + err.Error()}
	}
	// XReturnFormat md keeps the output clean; plain text is fine otherwise.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("X-Return-Format", "markdown")

	resp, err := client.Do(req)
	if err != nil {
		return Result{OK: false, Output: "reader fetch failed: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{OK: false, Output: fmt.Sprintf("http %d (reader) for %s — site is likely hard-blocked by a bot wall", resp.StatusCode, rawURL)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, webMaxBytes))
	if err != nil {
		return Result{OK: false, Output: "reader read failed: " + err.Error()}
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return Result{OK: false, Output: "reader returned empty content for " + rawURL}
	}
	if len(text) > webMaxChars {
		text = text[:webMaxChars] + "\n... (truncated)"
	}
	return Result{OK: true, Output: text}
}

// extractText strips script/style/head blocks and all tags, collapses the
// whitespace, and caps the result for the model.
func extractText(body string) string {
	text := unescapeAll(scriptStyleRe.ReplaceAllString(body, " "))
	text = collapseSpace(tagRe.ReplaceAllString(text, " "))
	text = spaceRe.ReplaceAllString(text, " ")
	text = blankRe.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > webMaxChars {
		text = text[:webMaxChars] + "\n... (truncated)"
	}
	return text
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
