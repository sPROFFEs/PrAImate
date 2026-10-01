package main

/*
#cgo webkit2_41 pkg-config: gtk+-3.0 webkit2gtk-4.1
#cgo !webkit2_41 pkg-config: gtk+-3.0 webkit2gtk-4.0
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

static volatile gint praimate_microphone_allowed = 0;
static gboolean praimate_media_permission(WebKitWebView *view, WebKitPermissionRequest *request, gpointer data) {
    if (!WEBKIT_IS_USER_MEDIA_PERMISSION_REQUEST(request)) return FALSE;
    const char *uri = webkit_web_view_get_uri(view);
    WebKitUserMediaPermissionRequest *media = WEBKIT_USER_MEDIA_PERMISSION_REQUEST(request);
    if (g_atomic_int_get(&praimate_microphone_allowed) && uri && g_str_has_prefix(uri, "wails://wails/") &&
        webkit_user_media_permission_is_for_audio_device(media) && !webkit_user_media_permission_is_for_video_device(media)) {
        webkit_permission_request_allow(request);
    } else {
        webkit_permission_request_deny(request);
    }
    return TRUE;
}
static void praimate_visit_webview(GtkWidget *widget, gpointer data) {
    if (WEBKIT_IS_WEB_VIEW(widget)) {
        if (g_object_get_data(G_OBJECT(widget), "praimate-voice-hook")) return;
        g_object_set_data(G_OBJECT(widget), "praimate-voice-hook", GINT_TO_POINTER(1));
        WebKitWebContext *context = webkit_web_view_get_context(WEBKIT_WEB_VIEW(widget));
        webkit_security_manager_register_uri_scheme_as_secure(webkit_web_context_get_security_manager(context), "wails");
        webkit_settings_set_enable_media_stream(webkit_web_view_get_settings(WEBKIT_WEB_VIEW(widget)), TRUE);
        g_signal_connect(widget, "permission-request", G_CALLBACK(praimate_media_permission), NULL);
    } else if (GTK_IS_CONTAINER(widget)) {
        gtk_container_foreach(GTK_CONTAINER(widget), praimate_visit_webview, NULL);
    }
}
static gboolean praimate_prepare_microphone(gpointer data) {
    GList *windows = gtk_window_list_toplevels();
    for (GList *entry = windows; entry; entry = entry->next) praimate_visit_webview(GTK_WIDGET(entry->data), NULL);
    g_list_free(windows);
    return G_SOURCE_REMOVE;
}
static void praimate_install_microphone_hook(void) { g_idle_add_full(G_PRIORITY_HIGH, praimate_prepare_microphone, NULL, NULL); }
static void praimate_prepare_voice_environment(void) {
    if (!gtk_init_check(NULL, NULL)) return;
    webkit_security_manager_register_uri_scheme_as_secure(webkit_web_context_get_security_manager(webkit_web_context_get_default()), "wails");
}
static void praimate_set_microphone_permission(int allowed) { g_atomic_int_set(&praimate_microphone_allowed, allowed); }
*/
import "C"

func prepareVoiceEnvironment()                           { C.praimate_prepare_voice_environment() }
func prepareVoiceCapture()                               { C.praimate_install_microphone_hook() }
func reserveNativeVoiceCapture(uint64)                   {}
func startNativeVoiceCapture(uint64) (bool, error)       { return false, nil }
func finishNativeVoiceCapture(id uint64) ([]byte, error) { return finishStudioMicrophone(id) }
func setVoiceCapturePermission(allowed bool) {
	value := 0
	if allowed {
		value = 1
	}
	C.praimate_set_microphone_permission(C.int(value))
	if !allowed {
		stopStudioMicrophone()
	}
}
