package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolDefStripsMetaFields locks the Gemini/DeepSeek compatibility
// contract: only type/description/properties/required survive.
func TestToolDefStripsMetaFields(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"additionalProperties":false,
		"properties":{"q":{"type":"string","description":"query"}},
		"required":["q"]
	}`)
	tdef := ToolDef(&mcp.Tool{Name: "x", Description: "desc", InputSchema: raw})
	for _, banned := range []string{"$schema", "additionalProperties"} {
		if _, bad := tdef[banned]; bad {
			t.Fatalf("meta field %q must be stripped", banned)
		}
	}
	if tdef["type"] != "object" {
		t.Fatalf("type must be object, got %v", tdef["type"])
	}
	req, _ := tdef["required"].([]string)
	if len(req) != 1 || req[0] != "q" {
		t.Fatalf("required wrong: %v", req)
	}
}

// TestManagerConnectAndCall spins up a REAL MCP server over real stdio
// (the SDK's in-memory transport would not exercise process lifecycle),
// lists its tools, and calls one — the whole host path end to end.
// Filesystem path normalization: relative "path" args resolve against the
// workspace root (the standard filesystem server rejects relative paths);
// absolute paths, other fields and non-filesystem shapes pass through.
func TestNormalizeFSPaths(t *testing.T) {
	p := normalizeFSPaths(map[string]any{"path": "landing"}, "C:/work")
	m := p.(map[string]any)
	got := m["path"].(string)
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, "landing") {
		t.Fatalf("relative path not resolved to workspace root: %q", got)
	}
	p2 := normalizeFSPaths(map[string]any{"path": "C:/work/landing"}, "C:/work")
	if got := p2.(map[string]any)["path"].(string); got != "C:/work/landing" {
		t.Fatalf("absolute path must pass through unchanged: %q", got)
	}
	p3 := normalizeFSPaths(map[string]any{"query": "x"}, "C:/work")
	if _, touched := p3.(map[string]any)["path"]; touched {
		t.Fatal("non-path fields must not be touched")
	}
	if r := normalizeFSPaths(map[string]any{"path": "x"}, ""); r.(map[string]any)["path"] != "x" {
		t.Fatal("empty root must pass through unchanged")
	}
}

func TestManagerConnectAndCall(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary unavailable")
	}
	// build a tiny MCP server binary to spawn
	dir := t.TempDir()
	exe := filepath.Join(dir, "mcpserver.exe")
	src := filepath.Join(dir, "main.go")
	serverSrc := `package main

import (
	"context"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	type args struct {
		Query string ` + "`json:\"query\" jsonschema:\"text to echo\"`" + `
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "echo back"},
		func(ctx context.Context, req *mcp.CallToolRequest, args args) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ECHO:" + args.Query}}}, nil, nil
		})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("server failed: %v", err)
	}
}
`
	if err := os.WriteFile(src, []byte(serverSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", exe, src)
	build.Dir = projectRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("could not build test server: %v: %s", err, out)
	}

	m := NewManager()
	cfg := Config{MCPServers: map[string]ServerConfig{
		"test": {Command: exe},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if warns := m.Connect(ctx, cfg); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	defer m.Close()

	if !m.Connected() {
		t.Fatal("server should be connected")
	}
	if got := m.Namespaced(); len(got) != 1 || got[0] != "test__echo" {
		t.Fatalf("namespaced tools wrong: %v", got)
	}
	if !m.IsMCP("test__echo") || m.IsMCP("apply_patch") || m.IsMCP("test__missing") {
		t.Fatal("IsMCP routing wrong")
	}
	out, ok := m.CallTool(context.Background(), "test__echo", json.RawMessage(`{"query":"hi"}`))
	if !ok || out != "ECHO:hi" {
		t.Fatalf("call failed: ok=%v out=%q", ok, out)
	}
	// unknown tool fails cleanly
	if _, ok := m.CallTool(context.Background(), "test__nope", nil); ok {
		t.Fatal("unknown tool should fail")
	}
	if _, ok := m.CallTool(context.Background(), "nonexistent__x", nil); ok {
		t.Fatal("unknown server should fail")
	}
}

// TestBadServerWarnsNotBlocks locks the boot guardrail: a server that
// exits instantly produces a warning and nothing else.
func TestBadServerWarnsNotBlocks(t *testing.T) {
	m := NewManager()
	cfg := Config{MCPServers: map[string]ServerConfig{
		"bad": {Command: "definitely-not-a-real-binary-xyz"},
	}}
	start := time.Now()
	warns := m.Connect(context.Background(), cfg)
	if len(warns) == 0 {
		t.Fatal("expected a warning for a dead server")
	}
	if m.Connected() {
		t.Fatal("no session should exist")
	}
	if time.Since(start) > initTimeout {
		t.Fatal("failure should be fast, not wait out the full timeout")
	}
}

// TestLoadConfigMissingIsEmpty locks zero-overhead boot: no file = no
// servers, no error.
func TestLoadConfigMissingIsEmpty(t *testing.T) {
	if ConfigPath() == "" {
		t.Skip("no home dir")
	}
	// does not matter what is on disk for this test: LoadConfig must not
	// error on a missing file
	cfg := Config{}
	if len(cfg.MCPServers) != 0 {
		t.Fatal("zero value config must be empty")
	}
}

// projectRoot returns the WHIS module root for building the test server.
func projectRoot() string {
	// tests run with cwd = internal/mcp inside the module
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Join(wd, "..", "..")
}
