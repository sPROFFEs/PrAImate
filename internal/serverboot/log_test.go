package serverboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongRunningServiceLogRemainsBounded(t *testing.T) {
	c := Configuration{Root: t.TempDir()}
	log, err := OpenLog(c)
	if err != nil {
		t.Fatal(err)
	}
	log.limit = 64
	defer log.Close()
	for i := 0; i < 100; i++ {
		if _, err = log.Write([]byte("diagnostic line\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = log.Write([]byte(strings.Repeat("x", 1000))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server.log", "server.log.previous"} {
		info, err := os.Stat(filepath.Join(c.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 64 {
			t.Fatal("service log grew beyond its rotation bound")
		}
	}
}
