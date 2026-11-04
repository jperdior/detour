package config

import (
	"math"
	"testing"
)

func TestDefaultParsesAndResolves(t *testing.T) {
	c, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	r, err := c.Resolve("bulk-reader", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Model.ID != "deepseek-flash" || r.APIKey != "sk-test" || r.Provider.BaseURL == "" {
		t.Fatalf("resolved: %+v", r)
	}
}

func TestResolveMissingKey(t *testing.T) {
	c, _ := Parse(Default())
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, err := c.Resolve("bulk-reader", ""); err == nil {
		t.Fatal("expected missing key error")
	}
}

func TestResolveModelOverrideKeyless(t *testing.T) {
	c, _ := Parse(Default())
	r, err := c.Resolve("bulk-reader", "qwen2.5-coder")
	if err != nil {
		t.Fatal(err)
	}
	if r.Model.ID != "qwen2.5-coder:14b" || r.APIKey != "" {
		t.Fatalf("resolved: %+v", r)
	}
}

func TestParseRejectsUnknownModel(t *testing.T) {
	_, err := Parse([]byte("providers: {}\nmodels: {}\nmodes:\n  x: {model: nope}\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPriceCost(t *testing.T) {
	p := Price{Input: 0.30, CachedInput: 0.006, Output: 1.20}
	got := p.Cost(1_000_000, 500_000, 100_000)
	want := 0.15 + 0.003 + 0.12
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v want %v", got, want)
	}
}
