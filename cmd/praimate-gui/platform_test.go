package main

import "testing"

func TestSupportedDesktopOS(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want bool
	}{
		{"linux", true},
		{"windows", true},
		{"darwin", true},
		{"freebsd", false},
	} {
		if got := supportedDesktopOS(tc.goos); got != tc.want {
			t.Errorf("supportedDesktopOS(%q) = %v, want %v", tc.goos, got, tc.want)
		}
	}
}
