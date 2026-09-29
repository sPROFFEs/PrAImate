package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionViewShowsHonestCompletionState(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, "Done"}, {context.Canceled, "Cancelled"}, {errors.New("failed"), "Failed"}} {
		var out bytes.Buffer
		v := sessionView{out: &out, width: 80}
		v.finish(time.Second, 2, nil, tc.err)
		if !strings.Contains(out.String(), tc.want) || (tc.err != nil && strings.Contains(out.String(), "Done")) {
			t.Fatal(out.String())
		}
	}
}

func TestSessionViewSanitizesMetadataAndRespectsNoColor(t *testing.T) {
	var out bytes.Buffer
	v := sessionView{out: &out, width: 80}
	v.banner("test", "work\x1b]52;c;injection\a", "session")
	v.session("local-model", "safe", nil, 2)
	if strings.ContainsAny(out.String(), "\x1b\a") || !strings.Contains(out.String(), "2 attached") {
		t.Fatal(out.String())
	}
}
