package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Agent struct {
	Enabled bool `json:"enabled"`
}

const (
	RuleAutoFollow        = "autoFollow"
	RuleNotifyCompleted   = "notifyCompleted"
	RuleCollapseOnOutside = "collapseOnOutside"
	RuleShowClock         = "showClock"
)

type Config struct {
	Agents map[string]Agent `json:"agents"`
	Rules  map[string]bool  `json:"rules"`
}

func DefaultRules() map[string]bool {
	return map[string]bool{
		RuleAutoFollow:        true,
		RuleNotifyCompleted:   true,
		RuleCollapseOnOutside: true,
		RuleShowClock:         true,
	}
}

func Default() Config {
	cfg := Config{Agents: map[string]Agent{}, Rules: DefaultRules()}
	for _, id := range []string{"qoder", "codex", "opencode", "claude-code", "zcode", "workbuddy", "deepseek-harness"} {
		cfg.Agents[id] = Agent{Enabled: true}
	}
	return cfg
}

func (c Config) AgentEnabled(id string) bool {
	if agent, ok := c.Agents[id]; ok {
		return agent.Enabled
	}
	return true
}

func (c Config) RuleEnabled(id string) bool {
	if value, ok := c.Rules[id]; ok {
		return value
	}
	if value, ok := DefaultRules()[id]; ok {
		return value
	}
	return true
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Nono", "config.json"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]Agent{}
	}
	if cfg.Rules == nil {
		cfg.Rules = DefaultRules()
	}
	for id, agent := range Default().Agents {
		if _, ok := cfg.Agents[id]; !ok {
			cfg.Agents[id] = agent
		}
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
