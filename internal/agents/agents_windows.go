package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"nono/internal/qoder"
)

type Info struct {
	Installed bool
	Path      string
	Version   string
}

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+[0-9A-Za-z.\-+]*`)

func Detect(id string) Info {
	switch id {
	case "qoder":
		info := qoder.Install()
		return Info{Installed: info.Installed, Path: info.Path, Version: info.Version}
	case "codex":
		return detect("codex", []string{
			filepath.Join(home(), ".codex", ".sandbox-bin", "codex.exe"),
			filepath.Join(appData(), "npm", "codex.cmd"),
		})
	case "workbuddy":
		return detectFile(filepath.Join(localAppData(), "Programs", "WorkBuddy", "WorkBuddy.exe"))
	case "deepseek-harness":
		return detectFile(filepath.Join(localAppData(), "Programs", "DeepSeek Harness", "DeepSeek Harness.exe"))
	case "opencode":
		return detect("opencode", []string{
			filepath.Join(appData(), "npm", "opencode.cmd"),
		})
	case "claude-code":
		return detect("claude", []string{
			filepath.Join(home(), ".claude", "local", "claude.exe"),
			filepath.Join(home(), ".local", "bin", "claude.exe"),
			filepath.Join(appData(), "npm", "claude.cmd"),
			filepath.Join(localAppData(), "Programs", "Claude", "Claude.exe"),
		})
	}
	return Info{}
}

func detect(name string, candidates []string) Info {
	for _, path := range candidates {
		if exists(path) {
			return Info{Installed: true, Path: path, Version: cliVersion(path)}
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return Info{Installed: true, Path: path, Version: cliVersion(path)}
	}
	return Info{}
}

func detectFile(path string) Info {
	if !exists(path) {
		return Info{}
	}
	return Info{Installed: true, Path: path, Version: fileVersion(path)}
}

func cliVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	var cmd *exec.Cmd
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", path, "--version")
	} else {
		cmd = exec.CommandContext(ctx, path, "--version")
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return versionPattern.FindString(string(out))
}

type fixedFileInfo struct {
	Signature        uint32
	StrucVersion     uint32
	FileVersionMS    uint32
	FileVersionLS    uint32
	ProductVersionMS uint32
	ProductVersionLS uint32
	FileFlagsMask    uint32
	FileFlags        uint32
	FileOS           uint32
	FileType         uint32
	FileSubtype      uint32
	FileDateMS       uint32
	FileDateLS       uint32
}

func fileVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return ""
	}
	var block unsafe.Pointer
	var length uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&block), &length); err != nil {
		return ""
	}
	if block == nil || length < uint32(unsafe.Sizeof(fixedFileInfo{})) {
		return ""
	}
	info := (*fixedFileInfo)(block)
	if info.Signature != 0xFEEF04BD {
		return ""
	}
	version := fmt.Sprintf("%d.%d.%d.%d",
		info.FileVersionMS>>16,
		info.FileVersionMS&0xFFFF,
		info.FileVersionLS>>16,
		info.FileVersionLS&0xFFFF)
	return strings.TrimSuffix(version, ".0")
}

func home() string {
	dir, _ := os.UserHomeDir()
	return dir
}

func appData() string {
	return os.Getenv("APPDATA")
}

func localAppData() string {
	return os.Getenv("LOCALAPPDATA")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
