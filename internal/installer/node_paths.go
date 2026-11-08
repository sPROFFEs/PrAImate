package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Node managers keep their installations outside the system PATH. Discover
// stable installation paths without sourcing a shell or retaining fnm's
// per-terminal links, which disappear after logout/reboot.
func userNodeBinDirs(home string) []string {
	var dirs []string
	add := func(dir string) {
		if !filepath.IsAbs(dir) {
			return
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		if _, err := exec.LookPath(filepath.Join(dir, "node")); err != nil {
			return
		}
		for _, previous := range dirs {
			if samePath(previous, dir) {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	// Managed startup records the stable runtime selected by the host GUI.
	// This is a path only; shell exports and transient multishell links are not
	// persisted. Older profiles simply use manager defaults below.
	add(os.Getenv("PRAIMATE_NODE_BIN"))
	pinned := len(dirs) > 0
	var nvmRoots, fnmRoots []string
	addRoot := func(roots *[]string, dir string) {
		if !filepath.IsAbs(dir) {
			return
		}
		for _, previous := range *roots {
			if samePath(previous, dir) {
				return
			}
		}
		*roots = append(*roots, dir)
	}
	config, _ := os.UserConfigDir()
	for _, root := range []string{os.Getenv("NVM_DIR"), filepath.Join(config, "nvm"), filepath.Join(home, ".config", "nvm"), filepath.Join(home, ".nvm")} {
		addRoot(&nvmRoots, root)
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	for _, root := range []string{os.Getenv("FNM_DIR"), filepath.Join(data, "fnm"), filepath.Join(home, ".fnm")} {
		addRoot(&fnmRoots, root)
	}
	if runtime.GOOS == "darwin" {
		addRoot(&fnmRoots, filepath.Join(home, "Library", "Application Support", "fnm"))
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"APPDATA", "LOCALAPPDATA"} {
			if root := os.Getenv(key); root != "" {
				addRoot(&fnmRoots, filepath.Join(root, "fnm"))
			}
		}
	}
	for _, root := range nvmRoots {
		versions := installedNodeVersions(filepath.Join(root, "versions", "node"))
		preferred := nvmDefaultVersion(root, versions)
		if preferred != "" {
			add(filepath.Join(root, "versions", "node", preferred, "bin"))
		}
		for _, version := range versions {
			add(filepath.Join(root, "versions", "node", version, "bin"))
		}
	}
	for _, root := range fnmRoots {
		bin := func(installation string) string {
			if runtime.GOOS == "windows" {
				return installation
			}
			return filepath.Join(installation, "bin")
		}
		add(bin(filepath.Join(root, "aliases", "default")))
		for _, version := range installedNodeVersions(filepath.Join(root, "node-versions")) {
			add(bin(filepath.Join(root, "node-versions", version, "installation")))
		}
	}
	if runtime.GOOS == "windows" {
		add(os.Getenv("NVM_SYMLINK"))
		root := os.Getenv("NVM_HOME")
		if filepath.IsAbs(root) {
			for _, version := range installedNodeVersions(root) {
				add(filepath.Join(root, version))
			}
		}
	}
	// Preserve a manager version already selected by the caller's shell. After
	// the first hydration, this also makes repeated rescans deterministic.
	if node, err := exec.LookPath("node"); err == nil && !pinned {
		if stable, err := filepath.EvalSymlinks(node); err == nil {
			active := filepath.Dir(stable)
			for i, dir := range dirs {
				if samePath(dir, active) {
					dirs = append([]string{dir}, append(dirs[:i], dirs[i+1:]...)...)
					break
				}
			}
		}
	}
	return dirs
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func nodeVersion(value string) ([]int, bool) {
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nil, false
	}
	numbers := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return nil, false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return nil, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		numbers[i] = n
	}
	return numbers, true
}

func installedNodeVersions(root string) []string {
	entries, _ := os.ReadDir(root)
	var versions []string
	for _, entry := range entries {
		if _, ok := nodeVersion(entry.Name()); ok && entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, _ := nodeVersion(versions[i])
		b, _ := nodeVersion(versions[j])
		for k := 0; k < 3; k++ {
			x, y := 0, 0
			if k < len(a) {
				x = a[k]
			}
			if k < len(b) {
				y = b[k]
			}
			if x != y {
				return x > y
			}
		}
		return versions[i] > versions[j]
	})
	return versions
}

func nvmDefaultVersion(root string, versions []string) string {
	alias := "default"
	for depth := 0; depth < 8; depth++ {
		path := filepath.Join(root, "alias", filepath.FromSlash(alias))
		info, err := os.Lstat(path)
		if err == nil && info.Mode().IsRegular() && info.Size() <= 256 {
			body, err := os.ReadFile(path)
			if err != nil {
				return ""
			}
			alias = strings.TrimSpace(string(body))
		} else {
			break
		}
		if prefix, ok := nodeVersion(alias); ok {
			for _, version := range versions {
				numbers, _ := nodeVersion(version)
				if len(numbers) < len(prefix) {
					continue
				}
				matches := true
				for i, n := range prefix {
					if numbers[i] != n {
						matches = false
					}
				}
				if matches {
					return version
				}
			}
			return ""
		}
		if alias == "node" || alias == "stable" {
			if len(versions) > 0 {
				return versions[0]
			}
			return ""
		}
		// Aliases are data, never shell expressions or arbitrary file paths.
		if strings.Contains(alias, "..") || strings.ContainsAny(alias, "\\\r\n\x00") || filepath.IsAbs(alias) || alias == "" {
			return ""
		}
	}
	return ""
}

// Explicitly promote the selected runtime even if it already occurs later in
// PATH. Merely prepending missing entries lets a rescan select another version.
func promoteNodeBinDirs(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	paths := append([]string(nil), dirs...)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		duplicate := false
		for _, dir := range dirs {
			if samePath(entry, dir) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			paths = append(paths, entry)
		}
	}
	_ = os.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
}
