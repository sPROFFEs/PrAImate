package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

func ValidateRemoteEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 4096 || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("knowledge endpoint must be an HTTP(S) base URL without embedded credentials, query or fragment")
	}
	return nil
}

type RemoteClient struct {
	Endpoint string
	APIKey   string
}
type RemoteResponse struct {
	Text     string `json:"text,omitempty"`
	Status   string `json:"status,omitempty"`
	HasIndex bool   `json:"hasIndex,omitempty"`
}

func (c RemoteClient) Call(ctx context.Context, action string, arguments any) (string, error) {
	if len(c.APIKey) > 8192 || strings.ContainsAny(c.APIKey, "\r\n\x00") {
		return "", errors.New("invalid knowledge API key")
	}
	if err := ValidateRemoteEndpoint(c.Endpoint); err != nil {
		return "", err
	}
	if action != "query" && action != "search" && action != "read" && action != "health" {
		return "", errors.New("unknown knowledge action")
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	method := http.MethodPost
	if action == "health" {
		method = http.MethodGet
		raw = nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+"/"+action, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("remote knowledge request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("remote knowledge returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(body) > 1<<20 {
		return "", errors.New("remote knowledge response exceeds 1 MiB")
	}
	var result RemoteResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return "", errors.New("invalid remote knowledge JSON response")
	}
	if action == "health" {
		if result.Status != "ok" {
			return "", errors.New("remote knowledge service is not healthy")
		}
		return "Remote knowledge service is available.", nil
	}
	if !utf8.ValidString(result.Text) || len(result.Text) > 32000 {
		return "", errors.New("remote knowledge result exceeds text limits")
	}
	// Enforce the caller's budget even when the server is a third party.
	if action == "query" {
		var args struct {
			Budget int `json:"budget"`
		}
		_ = json.Unmarshal(raw, &args)
		if args.Budget == 0 {
			args.Budget = 1200
		}
		if args.Budget < 200 || args.Budget > 8000 {
			return "", errors.New("query budget must be between 200 and 8000")
		}
		result.Text, _ = excerpt(Chunk{Text: result.Text}, nil, args.Budget*4)
	}
	return result.Text, nil
}

// HTTPHandler serves only knowledge reads. It does not expose index writes or
// execute models/commands. Set APIKey to require Bearer authentication.
func HTTPHandler(dir, apiKey string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		root, err := os.OpenRoot(dir)
		if err != nil {
			http.Error(w, "Knowledge root is unavailable", http.StatusServiceUnavailable)
			return
		}
		root.Close()
		writeRemote(w, RemoteResponse{Status: "ok", HasIndex: HasIndex(dir)})
	})
	for _, action := range []string{"query", "search", "read"} {
		mux.HandleFunc("POST /"+action, func(w http.ResponseWriter, r *http.Request) {
			body := http.MaxBytesReader(w, r.Body, 64<<10)
			defer body.Close()
			var args struct {
				Question   string `json:"question"`
				Budget     int    `json:"budget"`
				Query      string `json:"query"`
				Path       string `json:"path"`
				MaxResults int    `json:"max_results"`
				Offset     int64  `json:"offset"`
				Limit      int    `json:"limit"`
			}
			d := json.NewDecoder(body)
			d.DisallowUnknownFields()
			if err := d.Decode(&args); err != nil {
				http.Error(w, "Invalid knowledge request", 400)
				return
			}
			if err := d.Decode(new(any)); err != io.EOF {
				http.Error(w, "Expected one request object", 400)
				return
			}
			var text string
			var err error
			switch action {
			case "query":
				if args.Budget == 0 {
					args.Budget = 1200
				}
				text, err = Query(r.Context(), dir, args.Question, args.Budget)
			case "read":
				text, err = readRemoteSource(dir, args.Path, args.Offset, args.Limit)
			case "search":
				text, err = searchRemoteSources(r.Context(), dir, args.Query, args.Path, args.MaxResults)
			}
			if err != nil {
				http.Error(w, "Knowledge request failed: "+err.Error(), 400)
				return
			}
			writeRemote(w, RemoteResponse{Text: text})
		})
	}
	want := sha256.Sum256([]byte("Bearer " + apiKey))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiKey != "" {
			got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
			if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func writeRemote(w http.ResponseWriter, result RemoteResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func sourcePathAllowed(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || IsIndexDir(part) || part == ".git" || part == ".graphify" {
			return false
		}
	}
	return true
}
func readRemoteSource(dir, name string, offset int64, limit int) (string, error) {
	if !sourcePathAllowed(name) || offset < 0 {
		return "", errors.New("invalid knowledge source path or offset")
	}
	if limit <= 0 {
		limit = 16000
	}
	if limit > 32000 {
		return "", errors.New("read limit exceeds 32000 bytes")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return "", errors.New("knowledge source was not found")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("knowledge source must be a regular file")
	}
	f, err := openRegular(root, name, maxFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return "", err
	}
	for i := 0; i < 3 && len(body) > 0 && !utf8.RuneStart(body[0]); i++ {
		body = body[1:]
	}
	for i := 0; i < 3 && len(body) > 0 && !utf8.Valid(body); i++ {
		body = body[:len(body)-1]
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return "", errors.New("knowledge source is not UTF-8 text")
	}
	return string(body), nil
}
func searchRemoteSources(ctx context.Context, dir, query, prefix string, maxResults int) (string, error) {
	if strings.TrimSpace(query) == "" || len(query) > 1024 {
		return "", errors.New("search query must contain 1 to 1024 bytes")
	}
	if prefix != "" && !sourcePathAllowed(prefix) {
		return "", errors.New("invalid search path")
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 100 {
		return "", errors.New("too many search results")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	docs, contents, _, err := scan(ctx, root)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	count := 0
	for _, doc := range docs {
		if prefix != "" && doc.Path != prefix && !strings.HasPrefix(doc.Path, prefix+"/") {
			continue
		}
		for i, line := range strings.Split(string(contents[doc.Path]), "\n") {
			if strings.Contains(line, query) {
				if len(line) > 1000 {
					line, _ = excerpt(Chunk{Text: line}, questionTerms(query), 1000)
				}
				entry := fmt.Sprintf("%s:%d:%s\n", doc.Path, i+1, line)
				if out.Len()+len(entry) > 32000 {
					return out.String(), nil
				}
				out.WriteString(entry)
				count++
				if count >= maxResults {
					return out.String(), nil
				}
			}
		}
	}
	return out.String(), nil
}
