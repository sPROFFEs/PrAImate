package skills

import (
	"strings"
	"testing"
)

func TestPackageManifestPreservesBodyAndStandardMetadata(t *testing.T) {
	raw := "---\r\nname: review\r\ndescription: |\r\n  Review code.\r\nlicense: MIT\r\ncompatibility: No network required\r\nallowed-tools: Bash\r\nmetadata:\r\n  author: Example\r\nfuture: opaque\r\n---\r\n# Body\r\nKeep these bytes.\r\n"
	m, err := ParsePackageManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "review" || m.Description != "Review code.\n" || m.License != "MIT" || m.AllowedTools != "Bash" || m.Metadata["author"] != "Example" {
		t.Fatalf("manifest: %+v", m)
	}
	if m.Body != "# Body\r\nKeep these bytes.\r\n" {
		t.Fatalf("body rewritten: %q", m.Body)
	}
}

func TestPackageManifestRejectsInvalidAndAmplifyingYAML(t *testing.T) {
	for _, raw := range []string{
		"# no frontmatter", "---\nname: x\n", "---\nname: x\n---\nbody",
		"---\nname: x\nname: y\ndescription: text\n---\n",
		"---\nname: x\ndescription: &a [*a]\n---\n",
		"---\nname: x\ndescription: true\n---\n",
		"---\nname: ../x\ndescription: text\n---\n",
		"---\nname: x\ndescription: text\nmetadata: {approved: true}\n---\n",
		strings.Repeat("x", maxSkillMarkdown+1),
	} {
		if _, err := ParsePackageManifest([]byte(raw)); err == nil {
			t.Errorf("accepted invalid manifest of %d bytes", len(raw))
		}
	}
}
