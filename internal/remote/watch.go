package remote

import (
	"context"
	"github.com/fsnotify/fsnotify"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func (f *Files) Configure(ctx context.Context, publish func(string, any)) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	f.ctx = ctx
	f.publish = publish
	if f.watchers == nil {
		f.watchers = map[string]*fsnotify.Watcher{}
	}
}
func (f *Files) Watch(workspace string) error {
	if !filepath.IsAbs(workspace) {
		return Fail(400, "invalid_workspace", "An absolute workspace is required")
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if f.ctx == nil || f.publish == nil {
		return Fail(503, "watch_unavailable", "File notifications are unavailable")
	}
	if _, ok := f.watchers[workspace]; ok {
		return nil
	}
	if len(f.watchers) >= 8 {
		return Fail(429, "watch_limit", "Eight workspaces are already being monitored")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	added := map[string]bool{}
	add := func(directory string) error {
		return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(workspace, path)
			if rel != "." && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "vendor" || entry.Name() == "dist" || entry.Name() == "target" || strings.Count(rel, string(filepath.Separator)) > 8) {
				return fs.SkipDir
			}
			if added[path] {
				return nil
			}
			if len(added) >= 500 {
				return fs.SkipDir
			}
			if err := watcher.Add(path); err != nil {
				return err
			}
			added[path] = true
			return nil
		})
	}
	if err = add(workspace); err != nil {
		watcher.Close()
		return err
	}
	f.watchers[workspace] = watcher
	ctx := f.ctx
	publish := f.publish
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				rel, err := filepath.Rel(workspace, event.Name)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					for path := range added {
						if path == event.Name || strings.HasPrefix(path, event.Name+string(filepath.Separator)) {
							delete(added, path)
						}
					}
				}
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = add(event.Name)
					}
				}
				publish("praimate:editor-fs", map[string]string{"workspace": workspace, "rel": filepath.ToSlash(rel), "op": event.Op.String()})
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				publish("praimate:editor-fs", map[string]string{"workspace": workspace, "rel": "", "op": "RESYNC"})
			}
		}
	}()
	return nil
}
func (f *Files) Close() {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for workspace, watcher := range f.watchers {
		_ = watcher.Close()
		delete(f.watchers, workspace)
	}
}
