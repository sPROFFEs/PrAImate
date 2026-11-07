package remote

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (f *Files) Download(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace")
	path, err := safeRelative(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, err)
		return
	}
	if !filepath.IsAbs(workspace) {
		writeError(w, Fail(400, "invalid_workspace", "An absolute workspace is required"))
		return
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		writeError(w, err)
		return
	}
	defer root.Close()
	file, err := root.Open(path)
	if err != nil {
		writeError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, Fail(400, "invalid_file", "Select a regular file"))
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
func (g *Gateway) transferRoute(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v1/download" && r.URL.Path != "/v1/upload" {
		return false
	}
	if !Identity(r.Context()).Allows("admin") {
		writeError(w, Fail(403, "forbidden", "File transfer requires administrator permission"))
		return true
	}
	select {
	case g.calls <- struct{}{}:
		defer func() { <-g.calls }()
	default:
		writeError(w, Fail(429, "transfer_limit", "Too many active transfers"))
		return true
	}
	if r.URL.Path == "/v1/download" && (r.Method == "GET" || r.Method == "HEAD") && g.config.Files != nil {
		g.config.Files.Download(w, r)
		return true
	}
	if r.URL.Path == "/v1/upload" && r.Method == "PUT" && g.config.Upload != nil {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
		result, err := g.config.Upload(r.Context(), r.URL.Query().Get("name"), http.MaxBytesReader(w, r.Body, 512<<20))
		if err != nil {
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				err = Fail(413, "file_too_large", "Upload exceeds the 512 MiB transfer limit")
			}
			writeError(w, err)
		} else {
			writeJSON(w, result)
		}
		return true
	}
	http.NotFound(w, r)
	return true
}
