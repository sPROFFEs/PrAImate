package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
)

// PackageInstallReceipt records content only, never trust or activation.
// Receipt and objects are published in one directory rename. No external
// registry can observe a receipt for half-written objects.
type PackageInstallReceipt struct {
	Schema  string   `json:"schema"`
	Digests []string `json:"digests"`
}

// InstallPackages publishes a new immutable generation beneath an already
// private host-owned store. Existing generations are never overwritten.
// A crash before rename leaves only a .stage-* orphan (not an installation);
// a crash after rename leaves both receipt and complete objects together.
// P2 registry updates must reference the returned generation only on success.
func InstallPackages(ctx context.Context, store *os.Root, packages []SelectedPackage) (string, error) {
	return InstallPackagesWithLimits(ctx, store, packages, PackageLimits{})
}

func InstallPackagesWithLimits(ctx context.Context, store *os.Root, packages []SelectedPackage, limits PackageLimits) (string, error) {
	return installPackagesWithLimits(ctx, store, packages, limits, nil)
}

func installPackages(ctx context.Context, store *os.Root, packages []SelectedPackage, beforeCommit func() error) (string, error) {
	return installPackagesWithLimits(ctx, store, packages, PackageLimits{}, beforeCommit)
}

func installPackagesWithLimits(ctx context.Context, store *os.Root, packages []SelectedPackage, limits PackageLimits, beforeCommit func() error) (string, error) {
	limits, err := limits.normalized()
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(packages) == 0 {
		return "", errors.New("empty package installation")
	}
	receipt := PackageInstallReceipt{Schema: "praimate.skill-install/v1"}
	seen := make(map[string]bool)
	var total int64
	entries := 0
	for _, p := range packages {
		digest, err := PackageDigest(p.files)
		if err != nil {
			return "", err
		}
		if digest != p.digest || len(p.files) == 0 {
			return "", errors.New("selected package integrity mismatch")
		}
		if seen[digest] {
			continue
		}
		seen[digest] = true
		entrypoints := 0
		for _, f := range p.files {
			entries++
			if entries > limits.Entries || int64(len(f.Content)) > limits.FileBytes || int64(len(f.Content)) > limits.ExpandedBytes-total {
				return "", errors.New("installation exceeds host limits")
			}
			total += int64(len(f.Content))
			if path.Base(f.Path) == "SKILL.md" {
				if f.Path != "SKILL.md" {
					return "", errors.New("nested entrypoint in selected package")
				}
				if _, err := ParsePackageManifest(f.Content); err != nil {
					return "", err
				}
				entrypoints++
			}
		}
		if entrypoints != 1 {
			return "", errors.New("selected package requires one entrypoint")
		}
		receipt.Digests = append(receipt.Digests, digest)
	}
	sort.Strings(receipt.Digests)
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	identity := sha256.Sum256(data)
	final := "install-" + hex.EncodeToString(identity[:16])
	if _, err := store.Lstat(final); err == nil {
		if _, err := VerifyPackageInstallation(ctx, store, final, limits); err != nil {
			return "", err
		}
		if err := syncPackageDirectory(store, "."); err != nil {
			return final, err
		}
		return final, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(id[:])
	stage := ".stage-" + suffix
	if err := store.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = store.RemoveAll(stage)
		}
	}()
	staging, err := store.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	defer staging.Close()
	written := make(map[string]bool)
	directories := map[string]bool{".": true, "objects": true}
	for _, p := range packages {
		if written[p.digest] {
			continue
		}
		written[p.digest] = true
		base := "objects/" + p.digest[len("sha256:"):]
		for _, f := range p.files {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			name := base + "/" + f.Path
			if err := staging.MkdirAll(path.Dir(name), 0700); err != nil {
				return "", err
			}
			for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
				directories[parent] = true
			}
			// Scripts are stored non-executable; intent is preserved separately.
			if err := writePackageObject(staging, name, f.Content); err != nil {
				return "", err
			}
		}
		// Persist executable intent outside the resource tree for Windows.
		inventory := make([]struct {
			Path       string `json:"path"`
			Executable bool   `json:"executable,omitempty"`
		}, len(p.files))
		for i, f := range p.files {
			inventory[i].Path = f.Path
			inventory[i].Executable = f.Executable
		}
		data, err := json.Marshal(inventory)
		if err != nil {
			return "", err
		}
		if err := writePackageObject(staging, "objects/"+p.digest[len("sha256:"):]+".json", data); err != nil {
			return "", err
		}
	}
	if err := writePackageObject(staging, "receipt.json", data); err != nil {
		return "", err
	}
	orderedDirs := make([]string, 0, len(directories))
	for directory := range directories {
		orderedDirs = append(orderedDirs, directory)
	}
	sort.Slice(orderedDirs, func(i, j int) bool { return strings.Count(orderedDirs[i], "/") > strings.Count(orderedDirs[j], "/") })
	for _, directory := range orderedDirs {
		if directory != "." {
			if err := syncPackageDirectory(staging, directory); err != nil {
				return "", err
			}
		}
	}
	if err := syncPackageDirectory(staging, "."); err != nil {
		return "", err
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Close directory handles before rename for Windows compatibility.
	if err := staging.Close(); err != nil {
		return "", err
	}
	if err := store.Rename(stage, final); err != nil {
		// Another installer may have published the identical generation first.
		if _, verifyErr := VerifyPackageInstallation(ctx, store, final, limits); verifyErr == nil {
			if err := syncPackageDirectory(store, "."); err != nil {
				return final, err
			}
			return final, nil
		}
		return "", err
	}
	committed = true
	if err := syncPackageDirectory(store, "."); err != nil {
		return final, err
	}
	return final, nil
}

func writePackageObject(root *os.Root, name string, content []byte) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
