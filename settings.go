package main

import (
	"log"

	"github.com/egoist/mygo"

	"nono/internal/agents"
	"nono/internal/codex"
	"nono/internal/config"
	"nono/internal/qoder"
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
	{"codex", "Codex", "./assets/codex.png", true},
	{"opencode", "OpenCode", "./assets/opencode.png", true},
	{"claude-code", "Claude Code", "./assets/claude-code.png", true},
	{"zcode", "ZCode", "./assets/zcode.png", true},
	{"workbuddy", "WorkBuddy", "./assets/workbuddy.png", true},
	{"deepseek-harness", "DeepSeek Harness", "./assets/deepseek-harness.png", true},
}

type AgentToggle struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type SettingsSave struct {
	Agents []AgentToggle `json:"agents"`
	Rules  []AgentToggle `json:"rules"`
}

type RuleSetting struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

var ruleCatalog = []struct {
	ID          string
	Title       string
	Description string
}{
	{config.RuleAutoFollow, "自动跟随运行中的会话", "有会话开始运行时，刘海自动切换到该 agent 的会话；手动切换后 30 秒内不会被抢回"},
	{config.RuleNotifyCompleted, "任务结束通知", "任务完成时进入通知队列并自动切换到完成的会话"},
	{config.RuleCollapseOnOutside, "点击外部收起", "展开状态下点击刘海以外的区域自动收起"},
	{config.RuleShowClock, "显示时间", "在刘海右侧显示当前时间"},
}

func (n *Notch) RulesData() []RuleSetting {
	n.mu.Lock()
	cfg := n.cfg
	n.mu.Unlock()
	out := make([]RuleSetting, 0, len(ruleCatalog))
	for _, def := range ruleCatalog {
		out = append(out, RuleSetting{
			ID:          def.ID,
			Title:       def.Title,
			Description: def.Description,
			Enabled:     cfg.RuleEnabled(def.ID),
		})
	}
	return out
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

func (n *Notch) SaveSettings(payload SettingsSave) bool {
	knownAgents := map[string]bool{}
	for _, def := range agentCatalog {
		knownAgents[def.ID] = true
	}
	knownRules := map[string]bool{}
	for _, def := range ruleCatalog {
		knownRules[def.ID] = true
	}
	n.mu.Lock()
	if n.cfg.Agents == nil {
		n.cfg.Agents = map[string]config.Agent{}
	}
	if n.cfg.Rules == nil {
		n.cfg.Rules = config.DefaultRules()
	}
	for _, toggle := range payload.Agents {
		if knownAgents[toggle.ID] {
			n.cfg.Agents[toggle.ID] = config.Agent{Enabled: toggle.Enabled}
		}
	}
	for _, toggle := range payload.Rules {
		if knownRules[toggle.ID] {
			n.cfg.Rules[toggle.ID] = toggle.Enabled
		}
	}
	cfg := n.cfg
	n.mu.Unlock()

	if err := config.Save(cfg); err != nil {
		log.Printf("settings: save: %v", err)
		return false
	}
	n.collector.SetQoderEnabled(cfg.AgentEnabled(qoder.AgentID))
	n.codex.SetEnabled(cfg.AgentEnabled(codex.AgentID))
	for _, extra := range n.extras {
		extra.SetEnabled(cfg.AgentEnabled(extra.ID()))
	}
	notifyDone := cfg.RuleEnabled(config.RuleNotifyCompleted)
	n.collector.SetNotifyCompleted(notifyDone)
	n.codex.SetNotifyCompleted(notifyDone)
	for _, extra := range n.extras {
		extra.SetNotifyCompleted(notifyDone)
	}
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
