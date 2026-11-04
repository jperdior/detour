// Package config loads providers, models and modes. A mode is a declarative
// worker (instructions + model + parameters), like Spotify's AiKA modes.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultYAML []byte

// Default returns the embedded default config as YAML.
func Default() []byte { return defaultYAML }

// Provider types.
const (
	TypeOpenAI    = "openai"     // OpenAI-compatible HTTP endpoint (default)
	TypeClaudeCLI = "claude-cli" // `claude -p`: uses the Claude Code login, subscription included
)

// Provider is where a model runs.
type Provider struct {
	Type      string `yaml:"type"`        // openai (default) or claude-cli
	BaseURL   string `yaml:"base_url"`    // openai
	APIKeyEnv string `yaml:"api_key_env"` // openai; empty for keyless local servers
	Bin       string `yaml:"bin"`         // claude-cli; defaults to "claude"
}

// UsesClaudePlan reports whether calls consume the user's Claude usage
// (subscription quota or API spend), like the main agent does.
func (p Provider) UsesClaudePlan() bool { return p.Type == TypeClaudeCLI }

// Price is USD per million tokens.
type Price struct {
	Input       float64 `yaml:"input"`
	CachedInput float64 `yaml:"cached_input"`
	Output      float64 `yaml:"output"`
}

// Cost returns the USD cost of one call. cached is the part of prompt served
// from the provider's cache.
func (p Price) Cost(prompt, cached, completion int) float64 {
	uncached := prompt - cached
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*p.Input + float64(cached)*p.CachedInput + float64(completion)*p.Output) / 1e6
}

// Model ties a model id to a provider and a price.
type Model struct {
	Provider string `yaml:"provider"`
	ID       string `yaml:"id"` // id sent to the API; defaults to the map key
	Price    Price  `yaml:"price"`
}

// Mode is a declarative worker.
type Mode struct {
	Description     string  `yaml:"description"`
	Model           string  `yaml:"model"`
	Temperature     float64 `yaml:"temperature"`
	MaxOutputTokens int     `yaml:"max_output_tokens"`
	MaxInputTokens  int     `yaml:"max_input_tokens"`
	TimeoutSeconds  int     `yaml:"timeout_seconds"`
	Instructions    string  `yaml:"instructions"`
}

// Config is the whole file.
type Config struct {
	Providers map[string]Provider `yaml:"providers"`
	Models    map[string]Model    `yaml:"models"`
	Modes     map[string]Mode     `yaml:"modes"`
}

// Resolved is everything needed to call a mode.
type Resolved struct {
	Name     string
	Mode     Mode
	ModelKey string
	Model    Model
	Provider Provider
	APIKey   string
}

// Path is DETOUR_CONFIG or ~/.config/detour/config.yaml.
func Path() string {
	if p := os.Getenv("DETOUR_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "detour.yaml"
	}
	return filepath.Join(home, ".config", "detour", "config.yaml")
}

// Load reads the user config if present, else the embedded default.
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		data = defaultYAML
	} else if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses and validates a config.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for name, p := range c.Providers {
		switch p.Type {
		case "", TypeOpenAI, TypeClaudeCLI:
		default:
			return nil, fmt.Errorf("config: provider %q has unknown type %q", name, p.Type)
		}
	}
	for name, m := range c.Modes {
		mod, ok := c.Models[m.Model]
		if !ok {
			return nil, fmt.Errorf("config: mode %q uses unknown model %q", name, m.Model)
		}
		if _, ok := c.Providers[mod.Provider]; !ok {
			return nil, fmt.Errorf("config: model %q uses unknown provider %q", m.Model, mod.Provider)
		}
	}
	return &c, nil
}

// Resolve finds a mode, optionally overriding its model (DETOUR_MODEL or the
// model argument), and reads the API key from the environment.
func (c *Config) Resolve(mode, modelOverride string) (*Resolved, error) {
	m, ok := c.Modes[mode]
	if !ok {
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	key := m.Model
	if modelOverride != "" {
		key = modelOverride
	}
	mod, ok := c.Models[key]
	if !ok {
		return nil, fmt.Errorf("unknown model %q", key)
	}
	if mod.ID == "" {
		mod.ID = key
	}
	p, ok := c.Providers[mod.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", mod.Provider)
	}
	r := &Resolved{Name: mode, Mode: m, ModelKey: key, Model: mod, Provider: p}
	if p.APIKeyEnv != "" {
		r.APIKey = os.Getenv(p.APIKeyEnv)
		if r.APIKey == "" {
			return nil, fmt.Errorf("model %q needs the environment variable %s", key, p.APIKeyEnv)
		}
	}
	return r, nil
}
