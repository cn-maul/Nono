package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"

	"nono/internal/config"
	"nono/internal/qoder"
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
	collapsedWidth = 320
	// Windows enforces a minimum window height of 39 for this window style.
	collapsedHeight = 39
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
	mu        sync.Mutex
	window    *mygo.Window
	settings  *mygo.Window
	expanded  bool
	collector *qoder.Collector
	tray      *mygo.Tray
	cfg       config.Config
}

type UIState struct {
	qoder.View
	Expanded bool `json:"expanded"`
}

// Snapshot returns the current Qoder session state for the page.
func (n *Notch) Snapshot() UIState {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	view := n.collector.View(ctx)
	n.mu.Lock()
	expanded := n.expanded
	n.mu.Unlock()
	return UIState{View: view, Expanded: expanded}
}

// OpenQoder brings Qoder to the foreground.
func (n *Notch) OpenQoder() bool {
	var err error
	// Run on the UI thread: AttachThreadInput needs a thread with a message
	// queue to bypass Windows' foreground lock.
	mygo.RunOnMain(func() {
		err = qoder.FocusMainWindow()
	})
	if err != nil {
		log.Printf("notch: focus Qoder: %v", err)
		return false
	}
	return true
}

// Handle brings Qoder to the foreground and acknowledges its notice.
func (n *Notch) Handle(sessionID string) bool {
	if !n.OpenQoder() {
		return false
	}
	n.collector.AckNotice(sessionID)
	return true
}

// Skip acknowledges a notice without switching to Qoder.
func (n *Notch) Skip(sessionID string) bool {
	n.collector.AckNotice(sessionID)
	return true
}

// Toggle switches between the collapsed pill and the expanded card.
func (n *Notch) Toggle() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.expanded = !n.expanded
	n.applyBounds()
	return n.expanded
}

// Collapse returns the notch to the collapsed pill.
func (n *Notch) Collapse() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.expanded = false
	n.applyBounds()
	return false
}

// Cycle moves to the previous (-1) or next (1) conversation. M0 has no
// conversations yet, so it only keeps the single placeholder.
func (n *Notch) Cycle(dir int) int {
	return 0
}

// applyBounds sizes and centers the window under the top edge of its display.
// The caller holds n.mu.
func (n *Notch) applyBounds() {
	if n.window == nil {
		return
	}
	width, height := collapsedWidth, collapsedHeight
	if n.expanded {
		width, height = expandedWidth, expandedHeight
	}
	work := mygo.Screen.DisplayMatching(n.window.Bounds()).WorkArea
	n.window.SetBounds(mygo.Rectangle{
		X:      work.X + (work.Width-width)/2,
		Y:      work.Y,
		Width:  width,
		Height: height,
	})
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
	n := &Notch{collector: qoder.NewCollector(), cfg: cfg}
	n.collector.SetQoderEnabled(cfg.AgentEnabled("qoder"))
	defer n.collector.Close()
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
