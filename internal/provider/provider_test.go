package provider

import "testing"

func TestRegistryResolution(t *testing.T) {
	prov, wire, pr, err := Get("deepseek-v4-flash")
	if err != nil || prov != "deepseek" || wire != "deepseek-v4-flash" || pr.Out != 0.42 {
		t.Fatalf("Get(deepseek-v4-flash) = %v %v %v %v", prov, wire, pr, err)
	}
	if _, _, pr, _ = Get("deepseek-v4-pro"); pr.Context != 1_000_000 {
		t.Fatalf("deepseek-v4-pro context = %d, want 1M", pr.Context)
	}
	if _, _, _, err := Get("ollama:qwen2.5-coder:7b"); err != nil {
		t.Fatalf("ollama slug should resolve: %v", err)
	}
	if _, _, _, err := Get("or:vendor/model"); err != nil {
		t.Fatalf("openrouter slug should resolve: %v", err)
	}
	// live-fetched provider:wire ids resolve too
	prov, wire, err2 := func() (string, string, error) {
		p, w, _, e := Get("anthropic:claude-fable-5-1")
		return p, w, e
	}()
	if err2 != nil || prov != "anthropic" || wire != "claude-fable-5-1" {
		t.Fatalf("provider:wire slug = %v %v %v", prov, wire, err2)
	}
	if _, _, _, err := Get("nope"); err == nil {
		t.Fatal("unknown slug should error")
	}
	// retired aliases must be gone
	if Exists("deepseek-chat") || Exists("deepseek-reasoner") {
		t.Fatal("retired deepseek aliases still present")
	}
}

func TestCostMath(t *testing.T) {
	pr := Prices{In: 0.28, Cached: 0.028, Out: 0.42}
	got := pr.Cost(Usage{In: 1_000_000, Cached: 800_000, Out: 100_000})
	want := (200_000*0.28 + 800_000*0.028 + 100_000*0.42) / 1e6
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %f want %f", got, want)
	}
}

func TestHotSwapPreservesPrefixes(t *testing.T) {
	for _, slug := range []string{"ollama:llama3", "ollama-cloud:x", "or:y/z", "openai:gpt-6-sol"} {
		if !Exists(slug) {
			t.Fatalf("Exists(%q) = false", slug)
		}
	}
}
