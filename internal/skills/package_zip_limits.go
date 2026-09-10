package skills

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
)

// Preflight the central-directory framing before archive/zip allocates entries.
// The bounded profile deliberately rejects multi-disk, ZIP64 and self-extracting
// containers; supported packages are small enough not to require these formats.
// archive/zip remains responsible for decompression, local-header and CRC checks.
func preflightPackageZIP(ctx context.Context, source io.ReaderAt, size int64, limits PackageLimits) error {
	if size < 22 {
		return errors.New("truncated ZIP")
	}
	tailSize := int64(65557)
	if tailSize > size {
		tailSize = size
	}
	tail := make([]byte, int(tailSize))
	if _, err := source.ReadAt(tail, size-tailSize); err != nil {
		return err
	}
	end := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			end = i
			break
		}
	}
	if end < 0 {
		return errors.New("missing ZIP directory trailer")
	}
	record := tail[end:]
	count := int(binary.LittleEndian.Uint16(record[10:]))
	if binary.LittleEndian.Uint16(record[4:]) != 0 || binary.LittleEndian.Uint16(record[6:]) != 0 || int(binary.LittleEndian.Uint16(record[8:])) != count || count == 65535 {
		return errors.New("multi-disk/ZIP64 packages unsupported")
	}
	if count > limits.Entries {
		return errors.New("package entry limit exceeded")
	}
	directorySize := int64(binary.LittleEndian.Uint32(record[12:]))
	offset := int64(binary.LittleEndian.Uint32(record[16:]))
	trailer := size - tailSize + int64(end)
	if offset+directorySize != trailer {
		return errors.New("noncanonical ZIP directory bounds")
	}
	position := offset
	var header [46]byte
	var expanded int64
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if position+46 > trailer {
			return errors.New("truncated ZIP directory")
		}
		if _, err := source.ReadAt(header[:], position); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return errors.New("invalid ZIP directory entry")
		}
		if binary.LittleEndian.Uint16(header[34:]) != 0 {
			return errors.New("multi-disk ZIP entry unsupported")
		}
		uncompressed := int64(binary.LittleEndian.Uint32(header[24:]))
		if uncompressed > limits.FileBytes || uncompressed > limits.ExpandedBytes-expanded {
			return errors.New("expanded package limit exceeded")
		}
		expanded += uncompressed
		nameSize := int64(binary.LittleEndian.Uint16(header[28:]))
		extraSize := int64(binary.LittleEndian.Uint16(header[30:]))
		commentSize := int64(binary.LittleEndian.Uint16(header[32:]))
		next := position + 46 + nameSize + extraSize + commentSize
		if next > trailer {
			return errors.New("ZIP directory entry exceeds bounds")
		}
		extra := make([]byte, int(extraSize))
		if len(extra) > 0 {
			if _, err := source.ReadAt(extra, position+46+nameSize); err != nil {
				return err
			}
		}
		for len(extra) > 0 {
			if len(extra) < 4 {
				return errors.New("malformed ZIP extra field")
			}
			kind := binary.LittleEndian.Uint16(extra)
			length := int(binary.LittleEndian.Uint16(extra[2:]))
			if length > len(extra)-4 {
				return errors.New("truncated ZIP extra field")
			}
			if kind == 0x0001 || kind == 0x000d || kind == 0x756e {
				return errors.New("ZIP64 or Unix link metadata unsupported")
			}
			extra = extra[4+length:]
		}
		position = next
	}
	if position != trailer {
		return errors.New("ZIP directory count mismatch")
	}
	return ctx.Err()
}
