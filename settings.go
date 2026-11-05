package main

import (
	"log"

	"github.com/egoist/mygo"

	"nono/internal/agents"
	"nono/internal/config"
)

type AgentSetting struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Icon      string `json:"icon"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Supported bool   `json:"supported"`
}

var agentCatalog = []struct {
	ID        string
	Name      string
	Icon      string
	Supported bool
}{
	{"qoder", "Qoder", "./assets/qoder.png", true},
	{"codex", "Codex", "./assets/codex.png", false},
	{"workbuddy", "WorkBuddy", "./assets/workbuddy.png", false},
	{"deepseek-harness", "DeepSeek Harness", "./assets/deepseek-harness.png", false},
	{"opencode", "OpenCode", "./assets/opencode.png", false},
	{"claude-code", "Claude Code", "./assets/claude-code.png", false},
}

type AgentToggle struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func (n *Notch) SettingsData() []AgentSetting {
	n.mu.Lock()
	cfg := n.cfg
	n.mu.Unlock()
	out := make([]AgentSetting, 0, len(agentCatalog))
	for _, def := range agentCatalog {
		info := agents.Detect(def.ID)
		out = append(out, AgentSetting{
			ID:        def.ID,
			Name:      def.Name,
			Icon:      def.Icon,
			Path:      info.Path,
			Version:   info.Version,
			Installed: info.Installed,
			Enabled:   cfg.AgentEnabled(def.ID),
			Supported: def.Supported,
		})
	}
	return out
}

func (n *Notch) SaveSettings(toggles []AgentToggle) bool {
	known := map[string]bool{}
	for _, def := range agentCatalog {
		known[def.ID] = true
	}
	n.mu.Lock()
	if n.cfg.Agents == nil {
		n.cfg.Agents = map[string]config.Agent{}
	}
	for _, toggle := range toggles {
		if known[toggle.ID] {
			n.cfg.Agents[toggle.ID] = config.Agent{Enabled: toggle.Enabled}
		}
	}
	cfg := n.cfg
	n.mu.Unlock()

	if err := config.Save(cfg); err != nil {
		log.Printf("settings: save: %v", err)
		return false
	}
	n.collector.SetQoderEnabled(cfg.AgentEnabled("qoder"))
	n.closeSettings()
	return true
}

func (n *Notch) CancelSettings() bool {
	n.closeSettings()
	return true
}

func (n *Notch) OpenSettings() bool {
	n.Collapse()
	mygo.RunOnMain(func() {
		n.mu.Lock()
		win := n.settings
		n.mu.Unlock()
		if win != nil && !win.IsDestroyed() {
			win.Show()
			win.Focus()
			return
		}

		win = mygo.NewWindow(mygo.WindowOptions{
			Title:             "Nono 设置",
			URL:               "/settings.html",
			Width:             580,
			Height:            560,
			Hidden:            true,
			Frameless:         true,
			BackgroundColor:   "#0c0e12",
			CornerRadius:      12,
			SkipTaskbar:       true,
			DisableResize:     true,
			DisableMinimize:   true,
			DisableMaximize:   true,
			DisableFullScreen: true,
			DisableShadow:     true,
		})
		win.OnClosed(func() {
			n.mu.Lock()
			if n.settings == win {
				n.settings = nil
			}
			n.mu.Unlock()
		})
		n.mu.Lock()
		n.settings = win
		n.mu.Unlock()
		win.Show()
		win.Focus()
	})
	return true
}

func (n *Notch) closeSettings() {
	n.mu.Lock()
	win := n.settings
	n.settings = nil
	n.mu.Unlock()
	if win != nil && !win.IsDestroyed() {
		win.Close()
	}
}

// ShowMenu opens the notch's native context menu at the page coordinates.
func (n *Notch) ShowMenu(x, y int) {
	mygo.RunOnMain(func() {
		menu := mygo.NewMenu([]*mygo.MenuItem{
			{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) {
				n.OpenSettings()
			}},
			mygo.Separator(),
			{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) {
				mygo.App.Quit()
			}},
		})
		n.mu.Lock()
		win := n.window
		n.mu.Unlock()
		menu.PopupAt(win, x, y)
	})
}
