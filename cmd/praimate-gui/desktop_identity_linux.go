package main

// #cgo pkg-config: gtk+-3.0
// #include <gtk/gtk.h>
// static void praimate_identity(void) {
//     g_set_prgname("praimate");
//     g_set_application_name("PrAImate");
//     gtk_window_set_default_icon_name("praimate");
// }
import "C"

// Wails 2.10 sets ProgramName after constructing the first GTK window.
// Set it before GTK starts so WM_CLASS and the Wayland app ID match
// praimate.desktop even for Studio and detached windows.
func prepareDesktopIdentity() { C.praimate_identity() }
func setDesktopIcon(_ []byte) {}
