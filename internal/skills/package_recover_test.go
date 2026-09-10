package skills

import (
	"context"
	"os"
	"testing"
)

func TestVerifyInstallationRejectsUnlistedResources(t *testing.T) {
	for _, extra := range []string{"extra.txt", "objects/extra.txt", "bundle-extra.txt", "empty-directory"} {
		t.Run(extra, func(t *testing.T) {
			candidates := selectionFixture(t)
			plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			generation, err := InstallPackages(context.Background(), root, plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyPackageInstallation(context.Background(), root, generation, PackageLimits{}); err != nil {
				t.Fatal(err)
			}
			name := generation + "/" + extra
			if extra == "bundle-extra.txt" {
				name = generation + "/objects/" + plan[0].Digest()[7:] + "/extra.txt"
			}
			if extra == "empty-directory" {
				err = root.Mkdir(name, 0700)
			} else {
				err = root.WriteFile(name, []byte("unapproved resource"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyPackageInstallation(context.Background(), root, generation, PackageLimits{}); err == nil {
				t.Fatal("unlisted resource accepted")
			}
		})
	}
}
