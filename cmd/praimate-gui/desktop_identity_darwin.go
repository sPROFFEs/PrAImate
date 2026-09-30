package main

// #cgo LDFLAGS: -framework Cocoa
// #cgo CFLAGS: -fblocks
// void praimate_set_icon(const void *bytes, int length);
import "C"
import "unsafe"

func prepareDesktopIdentity() {}

// Also give direct binary launches a Dock icon, outside an application bundle.
func setDesktopIcon(icon []byte) {
	if len(icon) > 0 {
		C.praimate_set_icon(unsafe.Pointer(&icon[0]), C.int(len(icon)))
	}
}
