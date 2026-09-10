package skills

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// PackageFile is an acquired regular file, not a filesystem path to follow.
// Executable is portable manifest intent, never approval to execute content.
type PackageFile struct {
	Path       string
	Content    []byte
	Executable bool
}

// PackageDigest implements the project's sha256-tree-v1 contract. Acquisition
// must separately enforce type, size and link restrictions before calling it.
// Content bytes (including line endings) and executable intent are preserved.
func PackageDigest(files []PackageFile) (string, error) {
	ordered := append([]PackageFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	inventory := make(packageInventory)
	for _, file := range ordered {
		if err := inventory.add(file.Path, false); err != nil {
			return "", err
		}
	}
	h := sha256.New()
	h.Write([]byte("praimate-skill-tree-v1\x00"))
	var size [8]byte
	for _, file := range ordered {
		binary.BigEndian.PutUint64(size[:], uint64(len(file.Path)))
		h.Write(size[:])
		h.Write([]byte(file.Path))
		mode := byte(0)
		if file.Executable {
			mode = 1
		}
		h.Write([]byte{mode})
		binary.BigEndian.PutUint64(size[:], uint64(len(file.Content)))
		h.Write(size[:])
		h.Write(file.Content)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type packageInventoryEntry struct {
	name      string
	directory bool
	explicit  bool
}

// Include implicit parent directories: differing spellings must not merge on
// case-insensitive filesystems. Compatibility normalization and full case folding
// are comparison-only: accepted paths and content bytes are never rewritten.
type packageInventory map[string]packageInventoryEntry

func packagePathKey(name string) string {
	return cases.Fold().String(norm.NFKC.String(name))
}

func (inventory packageInventory) add(name string, directory bool) error {
	if err := validatePackagePath(name); err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		key := packagePathKey(prefix)
		explicit := i == len(parts)-1
		isDir := !explicit || directory
		if previous, exists := inventory[key]; exists {
			if previous.name != prefix || previous.directory != isDir || (explicit && previous.explicit) {
				return fmt.Errorf("ambiguous package path %q", name)
			}
			explicit = explicit || previous.explicit
		}
		inventory[key] = packageInventoryEntry{prefix, isDir, explicit}
	}
	return nil
}

func validatePackagePath(name string) error {
	if len(name) > 4096 || strings.Count(name, "/") >= 64 {
		return fmt.Errorf("package path length/depth exceeded")
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:<>\"|?*\x00") {
		return fmt.Errorf("non-portable package path %q", name)
	}
	if !norm.NFC.IsNormalString(name) {
		return fmt.Errorf("package path must use NFC normalization: %q", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("non-portable package path %q", name)
		}
	}
	for _, part := range strings.Split(name, "/") {
		if len(part) > 255 {
			return fmt.Errorf("package path component too long")
		}
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return fmt.Errorf("invalid package path %q", name)
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		runes := []rune(base)
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" ||
			(len(runes) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", runes[3])) {
			return fmt.Errorf("reserved package path %q", name)
		}
	}
	return nil
}
