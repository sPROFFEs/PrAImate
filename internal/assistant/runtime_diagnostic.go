package assistant

import (
	"debug/pe"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Runtime output stays in bounded RAM. It is shown only when startup fails;
// authentication is removed before it can reach the UI or encrypted state.
type runtimeDiagnostic struct {
	mu   sync.Mutex
	tail []byte
}

func (d *runtimeDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	const limit = 4096
	if n >= limit {
		d.tail = append(d.tail[:0], p[n-limit:]...)
	} else {
		if len(d.tail)+n > limit {
			d.tail = d.tail[len(d.tail)+n-limit:]
		}
		d.tail = append(d.tail, p...)
	}
	return n, nil
}

func (d *runtimeDiagnostic) detail(key string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := strings.ToValidUTF8(string(d.tail), "")
	if key != "" {
		text = strings.ReplaceAll(text, key, "[redacted]")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return ": " + text
}

func runtimeExitDescription(code int) string {
	switch uint32(code) {
	case 0xc0000135:
		return "0xC0000135: a required DLL is missing; install the Microsoft Visual C++ v14 Redistributable matching the runtime architecture"
	case 0xc000007b:
		return "0xC000007B: incompatible executable or DLL architecture; reinstall the matching platform runtime"
	case 0xc0000139:
		return "0xC0000139: a required DLL entry point is unavailable; repair or update the Microsoft Visual C++ v14 Redistributable"
	case 0xc000001d:
		return "0xC000001D: unsupported CPU instruction; check the CPU features exposed to this machine or VM"
	case 0xc0000142:
		return "0xC0000142: a runtime DLL could not initialize"
	default:
		return fmt.Sprintf("exit code %d", code)
	}
}

// Follow only imports inside the selected package; never search user PATH or
// execute a DLL. This also lets Linux tests inspect a published Windows package.
func runtimeCRTImports(entry string) ([]string, error) {
	root := filepath.Dir(entry)
	pending := []string{entry}
	seen, required := map[string]bool{}, map[string]bool{}
	for len(pending) > 0 {
		path := pending[0]
		pending = pending[1:]
		name := strings.ToLower(filepath.Base(path))
		if seen[name] {
			continue
		}
		seen[name] = true
		file, err := pe.Open(path)
		if err != nil {
			return nil, fmt.Errorf("inspect managed runtime %s: %w", filepath.Base(path), err)
		}
		// debug/pe.ImportedLibraries is a stub in Go 1.26. ImportedSymbols
		// contains function:DLL pairs and exposes the actual dependencies.
		imports, err := file.ImportedSymbols()
		file.Close()
		if err != nil {
			return nil, err
		}
		dllSeen := map[string]bool{}
		for _, symbol := range imports {
			separator := strings.LastIndexByte(symbol, ':')
			if separator < 0 {
				continue
			}
			dll := symbol[separator+1:]
			lower := strings.ToLower(dll)
			if dllSeen[lower] {
				continue
			}
			dllSeen[lower] = true
			if strings.ContainsAny(dll, `/\\`) || dll == "" {
				return nil, fmt.Errorf("invalid runtime DLL import %q", dll)
			}
			local := filepath.Join(root, dll)
			if info, err := os.Stat(local); err == nil && info.Mode().IsRegular() {
				pending = append(pending, local)
				continue
			}
			if strings.HasPrefix(lower, "vcruntime140") || strings.HasPrefix(lower, "msvcp140") {
				required[lower] = true
			}
		}
	}
	out := make([]string, 0, len(required))
	for dll := range required {
		out = append(out, dll)
	}
	sort.Strings(out)
	return out, nil
}
