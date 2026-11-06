package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"

	"nono/internal/claude"
	"nono/internal/codex"
	"nono/internal/config"
	"nono/internal/dsh"
	"nono/internal/hub"
	"nono/internal/opencode"
	"nono/internal/qoder"
	"nono/internal/workbuddy"
	"nono/internal/zcode"
)

//go:embed all:web
var webFiles embed.FS

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	getWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	setWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
)

const (
	wsExNoActivate = 0x08000000
	wsExToolWindow = 0x00000080
)

const (
	collapsedWidth = 352
	// Windows enforces a minimum window height of 39 for this window style.
	collapsedHeight = 46
	expandedWidth   = 560
	expandedHeight  = 224
)

// keepOutOfTheWay keeps the notch out of Alt+Tab and keeps clicks from
// taking focus away from the foreground app.
func keepOutOfTheWay(hwnd uintptr) {
	// GWL_EXSTYLE is -20, expressed as an unsigned pointer-sized value.
	index := ^uintptr(19)
	style, _, _ := getWindowLongPtr.Call(hwnd, index)
	setWindowLongPtr.Call(hwnd, index, style|wsExNoActivate|wsExToolWindow)
}

// Notch is the window shell exposed to the page.
type Notch struct {
	mu           sync.Mutex
	window       *mygo.Window
	settings     *mygo.Window
	expanded     bool
	animating    bool
	watchCancel  chan struct{}
	collector    *qoder.Collector
	codex        *codex.Collector
	extras       []*hub.Collector
	sessionAgent map[string]string
	tray         *mygo.Tray
	cfg          config.Config
}

type AgentBrief struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Enabled bool   `json:"enabled"`
}

type UIState struct {
	qoder.View
	Expanded   bool            `json:"expanded"`
	Agents     []AgentBrief    `json:"agents"`
	AnyRunning bool            `json:"anyRunning"`
	Rules      map[string]bool `json:"rules"`
}

// Snapshot returns the current sessions of every enabled agent for the page.
func (n *Notch) Snapshot() UIState {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	qv := n.collector.View(ctx)
	cv := n.codex.View(ctx)
	parts := make([]qoder.View, 0, 2+len(n.extras))
	parts = append(parts, qv, cv)
	for _, extra := range n.extras {
		parts = append(parts, extra.View(ctx))
	}
	view := mergeViews(parts)

	n.mu.Lock()
	expanded := n.expanded
	cfg := n.cfg
	sessions := make(map[string]string, len(view.Sessions))
	for _, session := range view.Sessions {
		sessions[session.ID] = session.AgentID
	}
	n.sessionAgent = sessions
	n.mu.Unlock()

	agents := []AgentBrief{
		{ID: qoder.AgentID, Name: qoder.AgentName, Running: qv.QoderRunning, Enabled: cfg.AgentEnabled(qoder.AgentID)},
		{ID: codex.AgentID, Name: codex.AgentName, Running: cv.QoderRunning, Enabled: cfg.AgentEnabled(codex.AgentID)},
	}
	anyRunning := qv.QoderRunning || cv.QoderRunning
	for i, extra := range n.extras {
		part := parts[2+i]
		agents = append(agents, AgentBrief{
			ID:      extra.ID(),
			Name:    extra.Name(),
			Running: part.QoderRunning,
			Enabled: cfg.AgentEnabled(extra.ID()),
		})
		anyRunning = anyRunning || part.QoderRunning
	}
	return UIState{
		View:       view,
		Expanded:   expanded,
		Agents:     agents,
		AnyRunning: anyRunning,
		Rules: map[string]bool{
			config.RuleAutoFollow:        cfg.RuleEnabled(config.RuleAutoFollow),
			config.RuleNotifyCompleted:   cfg.RuleEnabled(config.RuleNotifyCompleted),
			config.RuleCollapseOnOutside: cfg.RuleEnabled(config.RuleCollapseOnOutside),
			config.RuleShowClock:         cfg.RuleEnabled(config.RuleShowClock),
		},
	}
}

// mergeViews folds the per-agent snapshots into one list for the notch.
func mergeViews(parts []qoder.View) qoder.View {
	out := qoder.View{GeneratedAt: time.Now().UnixMilli()}
	disabled := len(parts) > 0
	for _, part := range parts {
		out.Sessions = append(out.Sessions, part.Sessions...)
		out.Notices = append(out.Notices, part.Notices...)
		if part.Error != "" {
			if out.Error != "" {
				out.Error += " · "
			}
			out.Error += part.Error
		}
		out.QoderRunning = out.QoderRunning || part.QoderRunning
		disabled = disabled && part.Disabled
	}
	out.Disabled = disabled

	sort.SliceStable(out.Sessions, func(i, j int) bool {
		pi := qoder.State(out.Sessions[i].State).Priority()
		pj := qoder.State(out.Sessions[j].State).Priority()
		if pi != pj {
			return pi > pj
		}
		return out.Sessions[i].LastActivity > out.Sessions[j].LastActivity
	})
	sort.SliceStable(out.Notices, func(i, j int) bool {
		return out.Notices[i].Since < out.Notices[j].Since
	})

	if len(out.Notices) > 0 {
		out.FocusedID = out.Notices[0].SessionID
	} else if len(out.Sessions) > 0 {
		// Prefer the highest-priority session (a running agent) over whichever
		// agent happened to be merged first.
		out.FocusedID = out.Sessions[0].ID
	}
	return out
}

// OpenAgent brings the app that owns the session to the foreground.
func (n *Notch) OpenAgent(sessionID string) bool {
	n.mu.Lock()
	agent := n.sessionAgent[sessionID]
	n.mu.Unlock()
	var err error
	// Run on the UI thread: AttachThreadInput needs a thread with a message
	// queue to bypass Windows' foreground lock.
	mygo.RunOnMain(func() {
		switch agent {
		case codex.AgentID:
			err = codex.FocusMainWindow()
		case qoder.AgentID:
			err = qoder.FocusMainWindow()
		default:
			for _, extra := range n.extras {
				if extra.ID() == agent {
					err = extra.Focus()
					break
				}
			}
			if err == nil {
				err = errors.New("未知 agent")
			}
		}
	})
	if err != nil {
		log.Printf("notch: focus %s: %v", agent, err)
		return false
	}
	return true
}

// Handle brings the session's app to the foreground and acknowledges its
// notice.
func (n *Notch) Handle(sessionID string) bool {
	if !n.OpenAgent(sessionID) {
		return false
	}
	n.ack(sessionID)
	return true
}

// Skip acknowledges a notice without switching apps.
func (n *Notch) Skip(sessionID string) bool {
	n.ack(sessionID)
	return true
}

func (n *Notch) ack(sessionID string) {
	n.collector.AckNotice(sessionID)
	n.codex.AckNotice(sessionID)
	for _, extra := range n.extras {
		extra.AckNotice(sessionID)
	}
}

// Toggle switches between the collapsed pill and the expanded card.
func (n *Notch) Toggle() bool {
	n.mu.Lock()
	next := !n.expanded
	n.mu.Unlock()
	return n.SetExpanded(next)
}

// Collapse returns the notch to the collapsed pill.
func (n *Notch) Collapse() bool {
	return n.SetExpanded(false)
}

// SetExpanded switches modes, tweens the native window between the pill and
// the card, and while expanded watches for clicks outside the notch.
func (n *Notch) SetExpanded(next bool) bool {
	n.mu.Lock()
	if n.animating {
		current := n.expanded
		n.mu.Unlock()
		return current
	}
	if n.expanded == next {
		n.mu.Unlock()
		return next
	}
	win := n.window
	n.animating = true
	if next {
		n.expanded = true
	} else if n.watchCancel != nil {
		close(n.watchCancel)
		n.watchCancel = nil
	}
	from := n.boundsFor(!next)
	to := n.boundsFor(next)
	n.mu.Unlock()

	if win == nil {
		n.mu.Lock()
		n.expanded = next
		n.animating = false
		n.mu.Unlock()
		return next
	}
	if next {
		syncPageExpanded(win, true)
	} else {
		syncPagePhase(win, "collapsing")
	}
	go n.animateWindow(win, next, from, to)
	return next
}

func (n *Notch) animateWindow(win *mygo.Window, next bool, from, to mygo.Rectangle) {
	duration := 260 * time.Millisecond
	if !next {
		// Let the card fade before the window shrinks around it.
		time.Sleep(90 * time.Millisecond)
		duration = 200 * time.Millisecond
	}
	interval := 14 * time.Millisecond
	frames := int(duration / interval)
	if frames < 2 {
		frames = 2
	}
	for i := 1; i <= frames; i++ {
		if win.IsDestroyed() {
			return
		}
		t := float64(i) / float64(frames)
		var eased float64
		if next {
			eased = 1 - math.Pow(1-t, 3)
		} else {
			eased = t * t * (3 - 2*t)
		}
		win.SetBounds(mygo.Rectangle{
			X:      lerpInt(from.X, to.X, eased),
			Y:      lerpInt(from.Y, to.Y, eased),
			Width:  lerpInt(from.Width, to.Width, eased),
			Height: lerpInt(from.Height, to.Height, eased),
		})
		time.Sleep(12 * time.Millisecond)
	}
	if win.IsDestroyed() {
		return
	}
	win.SetBounds(to)
	if !next {
		syncPageExpanded(win, false)
	}
	n.mu.Lock()
	n.expanded = next
	n.animating = false
	var cancel chan struct{}
	if next {
		cancel = make(chan struct{})
		n.watchCancel = cancel
	}
	watch := n.cfg.RuleEnabled(config.RuleCollapseOnOutside)
	n.mu.Unlock()
	if cancel != nil && watch {
		go n.watchOutsideClick(cancel)
	}
}

func lerpInt(from, to int, t float64) int {
	return int(math.Round(float64(from) + float64(to-from)*t))
}

// syncPagePhase drives the page's exit animation while the window shrinks.
func syncPagePhase(win *mygo.Window, phase string) {
	page := win.Page()
	if page == nil {
		return
	}
	go func() {
		if _, err := page.Eval(fmt.Sprintf("window.__nonoPhase && window.__nonoPhase(%q)", phase)); err != nil {
			log.Printf("notch: sync phase: %v", err)
		}
	}()
}

// syncPageExpanded keeps the page's layout in step when Go changes the mode,
// since the page otherwise only learns about it on its next poll.
func syncPageExpanded(win *mygo.Window, expanded bool) {
	page := win.Page()
	if page == nil {
		return
	}
	go func() {
		if _, err := page.Eval(fmt.Sprintf("window.__nonoExpanded && window.__nonoExpanded(%t)", expanded)); err != nil {
			log.Printf("notch: sync page: %v", err)
		}
	}()
}

// Cycle moves to the previous (-1) or next (1) conversation. M0 has no
// conversations yet, so it only keeps the single placeholder.
func (n *Notch) Cycle(dir int) int {
	return 0
}

// boundsFor centers the window under the top edge of its display.
func (n *Notch) boundsFor(expanded bool) mygo.Rectangle {
	width, height := collapsedWidth, collapsedHeight
	if expanded {
		width, height = expandedWidth, expandedHeight
	}
	var work mygo.Rectangle
	if n.window != nil {
		work = mygo.Screen.DisplayMatching(n.window.Bounds()).WorkArea
	} else {
		work = mygo.Screen.PrimaryDisplay().WorkArea
	}
	return mygo.Rectangle{
		X:      work.X + (work.Width-width)/2,
		Y:      work.Y,
		Width:  width,
		Height: height,
	}
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "probe":
			qoder.RunProbe(os.Args[2:])
			return
		case "focus":
			if err := qoder.FocusMainWindow(); err != nil {
				log.Fatal(err)
			}
			return
		case "windows":
			qoder.DebugWindows()
			return
		case "codex":
			codex.Probe()
			return
		case "codex-win":
			codex.DebugWindows()
			return
		case "agents":
			debugAgents()
			return
		}
	}

	if !acquireSingleInstance() {
		showMessage("Nono 已在运行。", "Nono")
		return
	}

	files, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}
	mygo.SetFrontend(files)

	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: %v", err)
	}
	n := &Notch{
		collector:    qoder.NewCollector(),
		codex:        codex.NewCollector(),
		sessionAgent: map[string]string{},
		cfg:          cfg,
		extras: []*hub.Collector{
			hub.New(opencode.New(), cfg.AgentEnabled(opencode.AgentID)),
			hub.New(claude.New(), cfg.AgentEnabled(claude.AgentID)),
			hub.New(zcode.New(), cfg.AgentEnabled(zcode.AgentID)),
			hub.New(workbuddy.New(), cfg.AgentEnabled(workbuddy.AgentID)),
			hub.New(dsh.New(), cfg.AgentEnabled(dsh.AgentID)),
		},
	}
	n.collector.SetQoderEnabled(cfg.AgentEnabled(qoder.AgentID))
	n.codex.SetEnabled(cfg.AgentEnabled(codex.AgentID))
	notifyDone := cfg.RuleEnabled(config.RuleNotifyCompleted)
	n.collector.SetNotifyCompleted(notifyDone)
	n.codex.SetNotifyCompleted(notifyDone)
	for _, extra := range n.extras {
		extra.SetNotifyCompleted(notifyDone)
	}
	defer n.collector.Close()
	defer n.codex.Close()
	mygo.Bind(n)

	mygo.App.WhenReady(func() {
		work := mygo.Screen.PrimaryDisplay().WorkArea
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:             "Nono",
			URL:               "/",
			Width:             collapsedWidth,
			Height:            collapsedHeight,
			X:                 work.X + (work.Width-collapsedWidth)/2,
			Y:                 work.Y,
			Hidden:            true,
			Frameless:         true,
			Transparent:       true,
			AlwaysOnTop:       true,
			SkipTaskbar:       true,
			DisableResize:     true,
			DisableMinimize:   true,
			DisableMaximize:   true,
			DisableFullScreen: true,
			DisableShadow:     true,
		})
		keepOutOfTheWay(win.NativeHandle())
		n.mu.Lock()
		n.window = win
		n.mu.Unlock()
		win.ShowInactive()

		if icon, err := fs.ReadFile(files, "assets/nono-32.png"); err == nil {
			menu := mygo.NewMenu([]*mygo.MenuItem{
				{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) {
					n.OpenSettings()
				}},
				{Label: "显示 Nono", Click: func(*mygo.MenuItem, *mygo.Window) {
					win.ShowInactive()
				}},
				mygo.Separator(),
				{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) {
					mygo.App.Quit()
				}},
			})
			if tray, err := mygo.NewTray(mygo.TrayOptions{
				ID:      "nono",
				Icon:    icon,
				ToolTip: "Nono",
				Menu:    menu,
			}); err != nil {
				log.Printf("tray: %v", err)
			} else {
				n.tray = tray
			}
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
