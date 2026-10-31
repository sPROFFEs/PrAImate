package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

func (d *detachedCoordinator) documentFolder(w *detachedWindow) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return w.folder
}

func (d *detachedCoordinator) updateDocumentFolder(chatID, folder string) {
	d.mu.Lock()
	var windows []*detachedWindow
	for _, w := range d.windows {
		if w.kind == "studio" && w.sessionID == chatID {
			w.folder = folder
			windows = append(windows, w)
		}
	}
	d.mu.Unlock()
	for _, w := range windows {
		d.startDocumentWatcher(w)
	}
}

// The main process owns a watcher per document window. Detached renderers have
// no Core and never run initializeUnlockedStore's legacy editor watcher.
func (d *detachedCoordinator) startDocumentWatcher(w *detachedWindow) {
	d.mu.Lock()
	if d.closed || w.kind != "studio" || w.folder == "" || (w.watchCancel != nil && w.watchFolder == w.folder) {
		d.mu.Unlock()
		return
	}
	if w.watchCancel != nil {
		w.watchCancel()
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		d.mu.Unlock()
		d.publish("studio", w.sessionID, "praimate:editor-fs", map[string]string{"error": "Cannot watch the document workspace: " + err.Error()})
		return
	}
	parent := d.app.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	w.watchCancel = cancel
	w.watchFolder = w.folder
	folder := w.folder
	d.mu.Unlock()
	go func() {
		defer watcher.Close()
		report := func(err error) {
			if ctx.Err() == nil {
				d.publish("studio", w.sessionID, "praimate:editor-fs", map[string]string{"error": "Cannot watch the document workspace: " + err.Error(), "folder": folder})
			}
		}
		emit := func(path, op string) {
			if ctx.Err() != nil {
				return
			}
			d.publish("studio", w.sessionID, "praimate:editor-fs", map[string]string{"path": path, "op": op, "folder": folder})
		}
		watchedDirs := 0
		addTree := func(root string) {
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if ctx.Err() != nil {
					return filepath.SkipAll
				}
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					return nil
				}
				if path != root && editorSkipDir(entry.Name()) {
					return filepath.SkipDir
				}
				if watchedDirs >= maxWatchDirs {
					return filepath.SkipDir
				}
				rel, relErr := filepath.Rel(folder, path)
				if relErr == nil && strings.Count(rel, string(filepath.Separator)) > maxWatchDepth {
					return filepath.SkipDir
				}
				if err := watcher.Add(path); err != nil {
					return err
				}
				watchedDirs++
				return nil
			})
			if err != nil {
				report(err)
			}
		}
		addTree(folder)
		// A ready snapshot also recovers a renderer whose first tree request
		// happened before connection hydration completed.
		emit("", "RESCAN")
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if editorSkipDir(filepath.Base(event.Name)) {
					continue
				}
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						addTree(event.Name)
						emit("", "RESCAN")
					}
				}
				rel, err := filepath.Rel(folder, event.Name)
				if err == nil {
					emit(filepath.ToSlash(rel), event.Op.String())
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				emit("", "RESCAN")
				report(err)
			}
		}
	}()
}
