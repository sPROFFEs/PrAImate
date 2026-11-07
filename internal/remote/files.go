package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/fsnotify/fsnotify"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const maxFileBytes = 8 << 20

type Files struct {
	mu       sync.Mutex
	watchMu  sync.Mutex
	watchers map[string]*fsnotify.Watcher
	ctx      context.Context
	publish  func(string, any)
}
type FileRequest struct {
	Workspace    string `json:"workspace"`
	Path         string `json:"path"`
	Target       string `json:"target,omitempty"`
	Data         string `json:"data,omitempty"`
	ExpectedHash string `json:"expectedHash,omitempty"`
	Create       bool   `json:"create,omitempty"`
	Recursive    bool   `json:"recursive,omitempty"`
}
type FileContent struct {
	Path string `json:"path"`
	Data string `json:"data"`
	Hash string `json:"hash"`
	Size int    `json:"size"`
}
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Symlink bool   `json:"symlink"`
}

func safeRelative(path string) (string, error) {
	if path == "" {
		return ".", nil
	}
	if strings.Contains(path, "\\") || strings.Contains(path, ":") || !fs.ValidPath(path) {
		return "", Fail(400, "invalid_path", "Path must stay inside the selected workspace")
	}
	return path, nil
}
func fileHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func readRoot(root *os.Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(raw) > maxFileBytes {
		return nil, Fail(413, "file_too_large", "File exceeds the 8 MiB transfer limit")
	}
	return raw, err
}
func (f *Files) Call(method string, p FileRequest) (any, error) {
	if !filepath.IsAbs(p.Workspace) {
		return nil, Fail(400, "invalid_workspace", "An absolute server workspace is required")
	}
	path, err := safeRelative(p.Path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.Workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "files.tree":
		out := []string{}
		err := fs.WalkDir(root.FS(), path, func(rel string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if len(out) >= 5000 {
				return Fail(413, "directory_too_large", "Workspace exceeds 5000 visible files")
			}
			if entry.IsDir() {
				if rel != "." && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "vendor" || entry.Name() == "dist" || entry.Name() == "target" || strings.Count(rel, "/") > 8) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink == 0 {
				out = append(out, rel)
			}
			return nil
		})
		return out, err
	case "files.list":
		dir, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		defer dir.Close()
		entries, err := dir.ReadDir(2001)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(entries) > 2000 {
			return nil, Fail(413, "directory_too_large", "Directory exceeds 2000 entries")
		}
		out := []FileEntry{}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			out = append(out, FileEntry{entry.Name(), entry.IsDir(), info.Size(), entry.Type()&os.ModeSymlink != 0})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	case "files.read":
		raw, err := readRoot(root, path)
		if err != nil {
			return nil, err
		}
		return FileContent{path, base64.StdEncoding.EncodeToString(raw), fileHash(raw), len(raw)}, nil
	case "files.mkdir":
		return true, root.MkdirAll(path, 0700)
	case "files.write":
		raw, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return nil, Fail(400, "invalid_file_data", "File data must be base64")
		}
		if len(raw) > maxFileBytes {
			return nil, Fail(413, "file_too_large", "File exceeds the 8 MiB transfer limit")
		}
		previous, err := readRoot(root, path)
		if p.Create {
			if !errors.Is(err, os.ErrNotExist) {
				return nil, Fail(409, "file_exists", "File already exists or cannot be created")
			}
		} else {
			if err != nil {
				return nil, err
			}
			if p.ExpectedHash == "" || p.ExpectedHash != fileHash(previous) {
				return nil, Fail(409, "file_changed", "The file changed; reload it before saving")
			}
		}
		token, err := randomID(12)
		if err != nil {
			return nil, err
		}
		temp := filepath.ToSlash(filepath.Join(filepath.Dir(path), ".praimate-write-"+token))
		stream, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer root.Remove(temp)
		_, err = stream.Write(raw)
		if err == nil {
			err = stream.Sync()
		}
		closeErr := stream.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		// Preserve an existing source file's executable bits.
		if info, err := root.Stat(path); err == nil && !p.Create {
			if err = root.Chmod(temp, info.Mode().Perm()); err != nil {
				return nil, err
			}
		}
		if p.Create {
			// Link has exclusive destination semantics; unlike Rename it cannot
			// overwrite a file created between the existence check and this write.
			if err = root.Link(temp, path); err != nil {
				return nil, err
			}
		} else {
			if err = root.Rename(temp, path); err != nil {
				return nil, err
			}
		}
		return FileContent{Path: path, Hash: fileHash(raw), Size: len(raw)}, nil
	case "files.rename":
		target, err := safeRelative(p.Target)
		if err != nil {
			return nil, err
		}
		if _, err = root.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return nil, Fail(409, "file_exists", "Rename destination already exists or cannot be accessed")
		}
		return true, root.Rename(path, target)
	case "files.remove":
		if path == "." {
			return nil, Fail(400, "invalid_path", "Cannot remove the workspace root")
		}
		if p.Recursive {
			return true, root.RemoveAll(path)
		}
		return true, root.Remove(path)
	default:
		return nil, Fail(404, "unknown_method", "Unknown file operation")
	}
}
