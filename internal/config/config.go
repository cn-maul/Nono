package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Agent struct {
	Enabled bool `json:"enabled"`
}

type Config struct {
	Agents map[string]Agent `json:"agents"`
}

func Default() Config {
	cfg := Config{Agents: map[string]Agent{}}
	for _, id := range []string{"qoder", "codex", "workbuddy", "deepseek-harness", "opencode", "claude-code"} {
		cfg.Agents[id] = Agent{Enabled: id == "qoder"}
	}
	return cfg
}

func (c Config) AgentEnabled(id string) bool {
	if agent, ok := c.Agents[id]; ok {
		return agent.Enabled
	}
	return id == "qoder"
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
