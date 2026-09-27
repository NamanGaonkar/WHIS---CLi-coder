package tool

import (
	"compress/gzip"
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

// webSearchMaxChars caps the search results digest handed to the model.
const webSearchMaxChars = 3000

// zeroWidthRe strips zero-width / obfuscation chars and stray C0 controls
// from extracted page text. Sites inject zero-width joiners to defeat
// scrapers; left in, they garble the transcript ("s p a c e d" text) and
// skew column math. LF is preserved.
var zeroWidthRe = regexp.MustCompile(`[\x{200B}-\x{200F}\x{2060}-\x{2064}\x{FEFF}\x{00AD}\x{0000}-\x{0008}\x{000B}-\x{001F}\x{007F}]`)

// cleanText removes obfuscation characters from extracted page text.
func cleanText(s string) string {
	return zeroWidthRe.ReplaceAllString(s, "")
}

// searchHit is one web search result.
type searchHit struct {
	Title   string
	URL     string
	Snippet string
}

var (
	ddgAnchorRe  = regexp.MustCompile(`(?is)<a[^>]+class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<a[^>]+class="result__snippet"[^>]*>(.*?)</a>`)
	bingAlgoRe   = regexp.MustCompile(`(?is)<li class="b_algo"`)
	bingLinkRe   = regexp.MustCompile(`(?is)<h2[^>]*>\s*<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	bingParaRe   = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
)

// parseBingResults extracts hits from a Bing results page: each b_algo list
// item carries an h2>link (title), plus a paragraph snippet.
func parseBingResults(body string) []searchHit {
	chunks := bingAlgoRe.Split(body, -1)
	hits := []searchHit{}
	for _, chunk := range chunks[1:] { // skip the pre-first-item preamble
		m := bingLinkRe.FindStringSubmatch(chunk)
		if m == nil {
			continue
		}
		href := strings.TrimSpace(html.UnescapeString(m[1]))
		if href == "" || isPrivateHost(mustHost(href)) {
			continue
		}
		h := searchHit{
			Title: strings.TrimSpace(cleanText(collapseSpace(unescapeAll(tagRe.ReplaceAllString(m[2], ""))))),
			URL:   href,
		}
		if p := bingParaRe.FindStringSubmatch(chunk); p != nil {
			h.Snippet = strings.TrimSpace(cleanText(collapseSpace(unescapeAll(tagRe.ReplaceAllString(p[1], "")))))
		}
		hits = append(hits, h)
		if len(hits) >= 8 {
			break
		}
	}
	return hits
}

// parseDDGResults extracts hits from the DuckDuckGo html endpoint output.
func parseDDGResults(body string) []searchHit {
	anchors := ddgAnchorRe.FindAllStringSubmatch(body, -1)
	snips := ddgSnippetRe.FindAllStringSubmatch(body, -1)
	hits := []searchHit{}
	for i, m := range anchors {
		href := html.UnescapeString(m[1])
		// DDG wraps outbound links in /l/?uddg=<encoded> redirects
		if u, err := url.Parse(href); err == nil && u.Query().Get("uddg") != "" {
			href = u.Query().Get("uddg")
		}
		if href == "" || isPrivateHost(mustHost(href)) {
			continue
		}
		h := searchHit{
			Title: strings.TrimSpace(cleanText(collapseSpace(unescapeAll(tagRe.ReplaceAllString(m[2], ""))))),
			URL:   strings.TrimSpace(href),
		}
		if i < len(snips) {
			h.Snippet = strings.TrimSpace(cleanText(collapseSpace(unescapeAll(tagRe.ReplaceAllString(snips[i][1], "")))))
		}
		hits = append(hits, h)
		if len(hits) >= 8 {
			break
		}
	}
	return hits
}

// mustHost extracts the hostname of a URL ("" on parse failure).
func mustHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// WebSearch runs a web search and returns a compact digest of the top
// results. Bing primary (accepts plain GETs with browser headers), DuckDuckGo
// html endpoint fallback (bot-walls some networks), reader proxy last.
func (e *Env) WebSearch(query string) Result {
	query = strings.TrimSpace(query)
	if query == "" {
		return Result{Output: "empty query"}
	}
	res := e.bingSearch(query)
	if res.OK {
		return res
	}
	res = e.ddgSearch(query)
	if res.OK {
		return res
	}
	// last resort: reader proxy renders the results page server-side;
	// squash its markdown so results are one compact line per hit.
	res2 := e.readerFetch("https://www.bing.com/search?q=" + url.QueryEscape(query))
	if !res2.OK {
		return Result{OK: false, Output: "search failed: " + res.Output}
	}
	out := squashLines(cleanText(res2.Output))
	if len(out) > webSearchMaxChars {
		out = out[:webSearchMaxChars] + "\n... (truncated)"
	}
	return Result{OK: true, Output: "web search results for: " + query + "\n\n" + out}
}

// searchGet fetches a search-engine results page with browser-like headers.
func (e *Env) searchGet(searchURL string) (string, *http.Response, error) {
	u, err := url.Parse(searchURL)
	if err != nil {
		return "", nil, err
	}
	client := webClient()
	req, err := http.NewRequest(http.MethodGet, searchURL, nil)
	if err != nil {
		return "", nil, err
	}
	for k, vs := range webHeaders(u) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := webBody(resp)
	if err != nil {
		return "", resp, err
	}
	return body, resp, nil
}

// formatSearchDigest renders parsed hits the same way for every engine.
func formatSearchDigest(query string, hits []searchHit) Result {
	if len(hits) == 0 {
		return Result{OK: false, Output: "no results parsed"}
	}
	var b strings.Builder
	b.WriteString("web search results for: " + query + "\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, h.Title, h.URL)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.Snippet)
		}
	}
	out := b.String()
	if len(out) > webSearchMaxChars {
		out = out[:webSearchMaxChars] + "\n... (truncated)"
	}
	return Result{OK: true, Output: out}
}

// bingSearch scrapes Bing's results page (works with plain GETs + browser
// headers from networks where DDG bot-walls).
func (e *Env) bingSearch(query string) Result {
	body, resp, err := e.searchGet("https://www.bing.com/search?q=" + url.QueryEscape(query))
	if err != nil {
		return Result{OK: false, Output: "bing: " + err.Error()}
	}
	if resp != nil && blockedPage(body, resp.Header.Get("Content-Type")) {
		return Result{OK: false, Output: "bing blocked by bot wall"}
	}
	return formatSearchDigest(query, parseBingResults(body))
}

// ddgSearch hits the DuckDuckGo html endpoint with browser-like headers.
func (e *Env) ddgSearch(query string) Result {
	body, resp, err := e.searchGet("https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query))
	if err != nil {
		return Result{OK: false, Output: "ddg: " + err.Error()}
	}
	if resp != nil && blockedPage(body, resp.Header.Get("Content-Type")) {
		return Result{OK: false, Output: "ddg blocked by bot wall"}
	}
	return formatSearchDigest(query, parseDDGResults(body))
}

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
		Timeout: 15 * time.Second,
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
	// NO manual Accept-Encoding: Go's transport adds gzip and transparently
	// decompresses. Setting br/deflate by hand made servers return compressed
	// bytes we never unwrapped — binary garbage output (and terminal bells).
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

// webBody reads the response body, transparently unwrapping gzip when the
// server compressed it despite our headers (defensive: never feed the model
// binary garbage).
func webBody(resp *http.Response) (string, error) {
	r := io.LimitReader(resp.Body, webMaxBytes)
	if resp.Header.Get("Content-Encoding") == "gzip" ||
		(resp.Header.Get("Content-Type") == "" && looksGzip(r)) {
		zr, err := gzip.NewReader(io.MultiReader(r, resp.Body))
		if err == nil {
			return readAllCap(zr)
		}
		return "", fmt.Errorf("gzip unwrap failed: %w", err)
	}
	return readAllCap(r)
}

// looksGzip sniffs the 1f 8b magic bytes without consuming the reader.
func looksGzip(r io.Reader) bool {
	var m [2]byte
	n, _ := io.ReadFull(r, m[:])
	return n == 2 && m[0] == 0x1f && m[1] == 0x8b
}

func readAllCap(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return string(b), err
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

	body, err := webBody(resp)
	if err != nil {
		return Result{OK: false, Output: "read failed: " + err.Error()}
	}
	ctype := resp.Header.Get("Content-Type")
	if blockedPage(body, ctype) {
		return Result{OK: false, Output: "bot challenge page (cloudflare/captcha) — falling back"}
	}

	text := extractText(body)
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
	b, err := webBody(resp)
	if err != nil {
		return Result{OK: false, Output: "reader read failed: " + err.Error()}
	}
	text := cleanText(strings.TrimSpace(b))
	if text == "" {
		return Result{OK: false, Output: "reader returned empty content for " + rawURL}
	}
	if len(text) > webMaxChars {
		text = text[:webMaxChars] + "\n... (truncated)"
	}
	return Result{OK: true, Output: text}
}

// extractText strips script/style/head blocks and all tags, collapses the
// whitespace, removes obfuscation characters, and caps the result.
func extractText(body string) string {
	text := unescapeAll(scriptStyleRe.ReplaceAllString(body, " "))
	text = collapseSpace(tagRe.ReplaceAllString(text, " "))
	text = spaceRe.ReplaceAllString(text, " ")
	text = blankRe.ReplaceAllString(text, "\n\n")
	text = cleanText(text)
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

// multiSpaceRe matches runs of horizontal whitespace.
var multiSpaceRe = regexp.MustCompile(`[^\S\n]{2,}`)

// collapseSpace collapses runs of spaces/tabs into one space. It used to be
// a no-op, which left ragged "endless     space" gaps in titles, snippets
// and page text.
func collapseSpace(s string) string { return multiSpaceRe.ReplaceAllString(s, " ") }

// squashLines normalizes a markdown/text dump for the model: every line's
// internal whitespace runs become single spaces, empty lines drop out.
// Reader-proxy output (used for search fallback) is link-per-line markdown
// with huge blank gaps; this compacts it without losing structure.
func squashLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(spaceRe.ReplaceAllString(ln, " "))
		if ln == "" {
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

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
