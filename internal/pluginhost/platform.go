package pluginhost

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sys/cpu"
)

var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type pluginFile struct {
	ID   string
	Path string
}

// PluginFileInfo describes a plugin binary selected by the host discovery rules.
type PluginFileInfo struct {
	ID   string
	Path string
}

// ValidatePluginID reports whether id can be used as a plugin configuration key.
func ValidatePluginID(id string) bool {
	return validPluginID(id)
}

func validPluginID(id string) bool {
	return pluginIDPattern.MatchString(id)
}

func pluginIDFromPath(path string) string {
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	for _, extension := range []string{".so", ".dylib", ".dll"} {
		if strings.HasSuffix(lowerBase, extension) {
			return base[:len(base)-len(extension)]
		}
	}
	return base
}

func pluginExtension(goos string) string {
	switch goos {
	case "darwin":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}

func selectPluginFiles(root string) ([]pluginFile, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "plugins"
	}

	candidates := candidateDirs(root, runtime.GOOS, runtime.GOARCH, cpuVariant())
	extension := pluginExtension(runtime.GOOS)
	selected := make([]pluginFile, 0)
	seen := make(map[string]struct{})
	for _, dir := range candidates {
		entries, errReadDir := os.ReadDir(dir)
		if errReadDir != nil {
			if os.IsNotExist(errReadDir) {
				continue
			}
			return nil, errReadDir
		}
		files := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry == nil || !entry.Type().IsRegular() {
				continue
			}
			if strings.HasSuffix(strings.ToLower(entry.Name()), extension) {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
		sort.Strings(files)
		for _, path := range files {
			id := pluginIDFromPath(path)
			if !validPluginID(id) {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			selected = append(selected, pluginFile{ID: id, Path: path})
		}
	}
	return selected, nil
}

// DiscoverPluginFiles returns plugin binaries selected by the current host discovery rules.
func DiscoverPluginFiles(root string) ([]PluginFileInfo, error) {
	files, errSelect := selectPluginFiles(root)
	if errSelect != nil {
		return nil, errSelect
	}
	out := make([]PluginFileInfo, 0, len(files))
	for _, file := range files {
		out = append(out, PluginFileInfo{
			ID:   file.ID,
			Path: file.Path,
		})
	}
	return out, nil
}

func candidateDirs(root, goos, goarch, variant string) []string {
	dirs := make([]string, 0, 3)
	if variant != "" {
		dirs = append(dirs, filepath.Join(root, goos, goarch+"-"+variant))
	}
	dirs = append(dirs, filepath.Join(root, goos, goarch))
	dirs = append(dirs, root)
	return dirs
}

func cpuVariant() string {
	if runtime.GOARCH != "amd64" {
		return ""
	}
	if cpu.X86.HasAVX512F && cpu.X86.HasAVX512BW && cpu.X86.HasAVX512CD && cpu.X86.HasAVX512DQ && cpu.X86.HasAVX512VL {
		return "v4"
	}
	if cpu.X86.HasAVX && cpu.X86.HasAVX2 && cpu.X86.HasBMI1 && cpu.X86.HasBMI2 && cpu.X86.HasFMA {
		return "v3"
	}
	if cpu.X86.HasSSE3 && cpu.X86.HasSSSE3 && cpu.X86.HasSSE41 && cpu.X86.HasSSE42 && cpu.X86.HasPOPCNT {
		return "v2"
	}
	return "v1"
}
