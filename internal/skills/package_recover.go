package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// VerifyPackageInstallation is the recovery gate before referencing a published
// generation. Staging directories are never accepted as installations.
// All stored bytes are rehashed, including portable executable intent.
func VerifyPackageInstallation(ctx context.Context, store *os.Root, generation string, limits PackageLimits) (PackageInstallReceipt, error) {
	var receipt PackageInstallReceipt
	if !strings.HasPrefix(generation, "install-") || len(generation) != 40 {
		return receipt, errors.New("invalid installation generation")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(generation, "install-")); err != nil {
		return receipt, errors.New("invalid installation generation")
	}
	limits, err := limits.normalized()
	if err != nil {
		return receipt, err
	}
	info, err := store.Lstat(generation)
	if err != nil {
		return receipt, err
	}
	if !info.IsDir() {
		return receipt, errors.New("installation must be a directory")
	}
	root, err := store.OpenRoot(generation)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return receipt, err
	}
	if !os.SameFile(info, opened) {
		return receipt, errors.New("installation directory changed")
	}
	read := func(name string, limit int64) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("non-regular installation resource")
		}
		body, _, err := readLocalPackageFile(ctx, root, name, info, limit)
		return body, err
	}
	body, err := read("receipt.json", 1<<20)
	if err != nil {
		return receipt, err
	}
	if err := decodePackageRecord(body, &receipt); err != nil {
		return PackageInstallReceipt{}, err
	}
	if receipt.Schema != "praimate.skill-install/v1" || len(receipt.Digests) == 0 || len(receipt.Digests) > limits.Entries {
		return PackageInstallReceipt{}, errors.New("invalid installation receipt")
	}
	if !sort.StringsAreSorted(receipt.Digests) {
		return PackageInstallReceipt{}, errors.New("noncanonical installation receipt")
	}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return PackageInstallReceipt{}, err
	}
	identity := sha256.Sum256(canonical)
	if generation != "install-"+hex.EncodeToString(identity[:16]) {
		return PackageInstallReceipt{}, errors.New("installation identity mismatch")
	}
	seen := make(map[string]bool)
	expected := map[string]bool{"receipt.json": false, "objects": true}
	var total int64
	entries := 0
	for _, digest := range receipt.Digests {
		if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 || seen[digest] {
			return PackageInstallReceipt{}, errors.New("invalid receipt digest")
		}
		if _, err := hex.DecodeString(digest[7:]); err != nil {
			return PackageInstallReceipt{}, err
		}
		seen[digest] = true
		expected["objects/"+digest[7:]+".json"] = false
		var inventory []struct {
			Path       string `json:"path"`
			Executable bool   `json:"executable,omitempty"`
		}
		body, err := read("objects/"+digest[7:]+".json", 1<<20)
		if err != nil {
			return PackageInstallReceipt{}, err
		}
		if err := decodePackageRecord(body, &inventory); err != nil {
			return PackageInstallReceipt{}, err
		}
		files := make([]PackageFile, 0, len(inventory))
		for _, item := range inventory {
			entries++
			if entries > limits.Entries {
				return PackageInstallReceipt{}, errors.New("installation entry limit exceeded")
			}
			if err := validatePackagePath(item.Path); err != nil {
				return PackageInstallReceipt{}, err
			}
			name := "objects/" + digest[7:] + "/" + item.Path
			expected[name] = false
			for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
				expected[parent] = true
			}
			limit := limits.FileBytes
			if remaining := limits.ExpandedBytes - total; limit > remaining {
				limit = remaining
			}
			body, err := read("objects/"+digest[7:]+"/"+item.Path, limit)
			if err != nil {
				return PackageInstallReceipt{}, err
			}
			total += int64(len(body))
			files = append(files, PackageFile{Path: item.Path, Content: body, Executable: item.Executable})
		}
		actual, err := PackageDigest(files)
		if err != nil {
			return PackageInstallReceipt{}, err
		}
		if actual != digest {
			return PackageInstallReceipt{}, errors.New("installation integrity mismatch")
		}
	}
	if err := verifyPackageInventory(ctx, root, expected); err != nil {
		return PackageInstallReceipt{}, err
	}
	return receipt, nil
}

// Enumerate one entry at a time; an injected directory with millions of names
// must not allocate a complete listing before its first unexpected entry fails.
func verifyPackageInventory(ctx context.Context, root *os.Root, expected map[string]bool) error {
	directories := []string{"."}
	for name, directory := range expected {
		if directory {
			directories = append(directories, name)
		}
	}
	observed := make(map[string]bool, len(expected))
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("installation directory replaced")
		}
		f, err := root.Open(directory)
		if err != nil {
			return err
		}
		err = func() error {
			defer f.Close()
			opened, err := f.Stat()
			if err != nil {
				return err
			}
			if !os.SameFile(info, opened) {
				return errors.New("installation directory changed")
			}
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				batch, err := f.ReadDir(1)
				if err != nil && err != io.EOF {
					return err
				}
				for _, entry := range batch {
					name := path.Join(directory, entry.Name())
					isDir, exists := expected[name]
					if !exists || observed[name] || entry.IsDir() != isDir || entry.Type()&os.ModeSymlink != 0 {
						return errors.New("unexpected installation resource")
					}
					observed[name] = true
				}
				if err == io.EOF {
					return nil
				}
			}
		}()
		if err != nil {
			return err
		}
	}
	if len(observed) != len(expected) {
		return errors.New("incomplete installation inventory")
	}
	return nil
}

func decodePackageRecord(body []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing installation record data")
	}
	return nil
}
