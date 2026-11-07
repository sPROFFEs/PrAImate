package cloudflared

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestServiceDefinitionsPreserveLiteralPaths(t *testing.T) {
	root := "/profile/space 'quote' & $HOME %value"
	for _, platform := range []string{"windows", "darwin"} {
		p, err := servicePlan(platform, root, "praimate-test", 24567)
		if err != nil {
			t.Fatal(err)
		}
		d := xml.NewDecoder(strings.NewReader(p.Content))
		for {
			_, err = d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s service XML: %v", platform, err)
			}
		}
		if !strings.Contains(p.Content, "--token-file") {
			t.Fatal("missing private token file")
		}
	}
	p, err := servicePlan("linux", root, "praimate-test", 24567)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Content, "$$HOME") || !strings.Contains(p.Content, "%%value") {
		t.Fatal("systemd would expand a literal profile path")
	}
}
