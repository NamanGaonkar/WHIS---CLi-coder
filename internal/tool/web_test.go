package tool

import (
	"strings"
	"testing"
)

func TestWebExtractReadableText(t *testing.T) {
	page := []byte(`<html><head><style>body{color:red}</style><script>evil()</script></head>
<body><h1>Hello   World</h1><p>Line one</p><p>Line two</p></body></html>`)
	scriptStyleRe2 := scriptStyleRe
	tagRe2 := tagRe
	text := unescapeAll(scriptStyleRe2.ReplaceAllString(string(page), " "))
	text = collapseSpace(tagRe2.ReplaceAllString(text, " "))
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "Line one") {
		t.Fatalf("readable text lost: %q", text)
	}
	if strings.Contains(text, "evil") || strings.Contains(text, "color:red") {
		t.Fatalf("scripts/styles leaked: %q", text)
	}
}

func TestWebPrivateHostBlocked(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "10.0.0.5", "192.168.1.1", "172.16.0.1", "169.254.1.1"} {
		if !isPrivateHost(h) {
			t.Fatalf("private host %s not blocked", h)
		}
	}
	for _, h := range []string{"example.com", "raw.githubusercontent.com"} {
		if isPrivateHost(h) {
			t.Fatalf("public host %s wrongly blocked", h)
		}
	}
}

func TestWebFetchEmptyURL(t *testing.T) {
	e := NewEnv(".")
	r := e.WebFetch("   ")
	if r.OK {
		t.Fatal("empty url must not be OK")
	}
}
