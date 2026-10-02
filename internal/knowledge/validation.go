package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Bounded striped locks prevent simultaneous rebuilds of the same corpus.
// Waiting for a lock remains cancellable; no goroutine or lock map grows per path.
var indexLocks = func() [64]chan struct{} {
	var locks [64]chan struct{}
	for i := range locks {
		locks[i] = make(chan struct{}, 1)
		locks[i] <- struct{}{}
	}
	return locks
}()

func canonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}
func lockIndex(ctx context.Context, dir string) (func(), error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(canonicalDir(dir)))
	lock := indexLocks[h.Sum64()%uint64(len(indexLocks))]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-lock:
		return func() { lock <- struct{}{} }, nil
	}
}

// This cache attests only indexes verified against their original files in
// this process. Imported JSON cannot declare itself verified. Checksums of both
// the source corpus and snapshot invalidate it, even when timestamps are reused.
var verifiedIndexes = struct {
	sync.Mutex
	entries map[string][2]string
}{entries: map[string][2]string{}}

func indexVerified(dir, disk, source string) bool {
	key := canonicalDir(dir)
	verifiedIndexes.Lock()
	defer verifiedIndexes.Unlock()
	return verifiedIndexes.entries[key] == [2]string{disk, source}
}
func rememberVerified(dir, disk, source string) {
	key := canonicalDir(dir)
	verifiedIndexes.Lock()
	defer verifiedIndexes.Unlock()
	if len(verifiedIndexes.entries) >= 64 {
		clear(verifiedIndexes.entries)
	}
	verifiedIndexes.entries[key] = [2]string{disk, source}
}
func sourceFingerprint(docs []Document, skipped []string, graph string) string {
	raw, _ := json.Marshal(struct {
		Documents []Document
		Skipped   []string
		Graph     string
	}{docs, skipped, graph})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func graphifyDigest(dir string) string {
	return fileDigest(dir, "graphify-out/graph.json", maxIndex)
}
func fileDigest(dir, name string, limit int64) string {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ""
	}
	defer root.Close()
	f, err := openRegular(root, name, limit)
	if err != nil {
		return ""
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}
