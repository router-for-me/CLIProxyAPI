package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// environment holds the probes used to decide whether in-place updates are supported; tests replace them.
type environment struct {
	goos         string
	exePath      string
	hasTrigger   bool
	writable     func(dir string) error
	fileExists   func(path string) bool
	readFile     func(path string) ([]byte, error)
	platformImpl bool
}

func defaultEnvironment() environment {
	return environment{
		goos:         runtime.GOOS,
		exePath:      startupExe(),
		hasTrigger:   restartTriggerSet(),
		writable:     isWritableDir,
		fileExists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		readFile:     os.ReadFile,
		platformImpl: platformSupported,
	}
}

func inDocker(env environment) bool {
	if env.fileExists("/.dockerenv") || env.fileExists("/run/.containerenv") {
		return true
	}
	if data, err := env.readFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		for _, marker := range []string{"docker", "kubepods", "containerd", "libpod"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}

// checkSupported returns ("", true) when this process can update itself in place, otherwise a reason
// that also says how to update instead.
func checkSupported(env environment) (string, bool) {
	if env.goos != "linux" && env.goos != "darwin" || !env.platformImpl {
		return "in-place update is supported on Linux and macOS only; download the new release from https://github.com/" + repo + "/releases", false
	}
	if env.goos == "linux" && inDocker(env) {
		return "running in a container; pull the new image and recreate the container instead", false
	}
	if env.exePath == "" {
		return "cannot locate the running executable; re-run install.sh to update", false
	}
	if strings.Contains(filepath.ToSlash(env.exePath), "/Cellar/") {
		return "installed by Homebrew; update with your package manager (brew upgrade) instead", false
	}
	if !env.hasTrigger {
		return "not running as the proxx server (TUI or embedded mode); restart it manually after updating", false
	}
	if err := env.writable(filepath.Dir(env.exePath)); err != nil {
		return "the install directory " + filepath.Dir(env.exePath) + " is not writable by proxx; re-run install.sh (or fix its permissions) to update", false
	}
	return "", true
}
