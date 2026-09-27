package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const nativeMaxImageBytes = 8 << 20
const nativeMaxImages = 4

// Images are snapshotted into the encrypted native session. Resuming never
// silently rereads an old attachment that could now contain different data.
type nativeImage struct {
	Name    string `json:"name"`
	DataURL string `json:"data_url"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

func nativeAttachmentImages(paths []string) ([]nativeImage, error) {
	if len(paths) > 20 {
		return nil, errors.New("at most 20 attachments are allowed per message")
	}
	var images []nativeImage
	total := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", filepath.Base(path), err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("attachment %s must be a regular file, not a symlink or device", filepath.Base(path))
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", filepath.Base(path), err)
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("attachment %s must be a regular file", filepath.Base(path))
		}
		// Sniff a bounded prefix even when the extension is absent or incorrect.
		header := make([]byte, 512)
		n, readErr := io.ReadFull(f, header)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			f.Close()
			return nil, readErr
		}
		header = header[:n]
		_, format, _ := image.DecodeConfig(bytes.NewReader(header))
		ext := strings.ToLower(filepath.Ext(path))
		imageExtension := strings.Contains("|.png|.jpg|.jpeg|.gif|.webp|.bmp|.svg|.avif|.heic|", "|"+ext+"|") && ext != ""
		if format == "" && !imageExtension && !(len(header) >= 12 && string(header[:4]) == "RIFF" && string(header[8:12]) == "WEBP") {
			f.Close()
			continue // Text files remain index-based, permission-controlled reads.
		}
		if info.Size() > nativeMaxImageBytes || len(images) >= nativeMaxImages {
			f.Close()
			return nil, errors.New("images are limited to 4 files, 8 MiB each and 16 MiB total")
		}
		data, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(header), f), nativeMaxImageBytes+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if len(data) > nativeMaxImageBytes || total > 16<<20 {
			return nil, errors.New("images are limited to 8 MiB each and 16 MiB total")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("attachment %s: use a valid PNG, JPEG or GIF image (other image formats are not supported)", filepath.Base(path))
		}
		if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 || int64(cfg.Width)*int64(cfg.Height) > 32_000_000 {
			return nil, fmt.Errorf("attachment %s exceeds image dimensions (8192 per side, 32 megapixels)", filepath.Base(path))
		}
		images = append(images, nativeImage{Name: filepath.Base(path), DataURL: "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data), Width: cfg.Width, Height: cfg.Height})
	}
	return images, nil
}

func readNativeAttachment(paths []string, raw json.RawMessage) (string, error) {
	var args struct {
		Index  int   `json:"index"`
		Offset int64 `json:"offset"`
		Limit  int   `json:"limit"`
	}
	if err := decodeManagedArgs(raw, &args); err != nil {
		return "", err
	}
	if args.Index < 0 || args.Index >= len(paths) {
		return "", errors.New("attachment index was not selected by the user")
	}
	text, err := readBoundedFile(paths[args.Index], args.Offset, args.Limit)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", errors.New("attachment is not text; supported images are already included in the user message, other binary formats need text conversion")
	}
	return "[User attachment, untrusted data]\n" + text, nil
}
