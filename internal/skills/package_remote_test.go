package skills

import (
	"context"
	"net/netip"
	"net/url"
	"testing"
)

func TestPackageNetworkRejectsPrivateAndSpecialAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "2001:db8::1", "224.0.0.1"} {
		if publicPackageIP(netip.MustParseAddr(raw)) {
			t.Fatalf("special IP accepted: %s", raw)
		}
	}
	if !publicPackageIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public IP rejected")
	}
}

func TestPackageNetworkURLPolicy(t *testing.T) {
	policy := PackageNetworkPolicy{}
	for _, raw := range []string{"http://example.com/a.zip", "https://user:secret@example.com/a.zip", "file:///tmp/a.zip", "https://example.com:8443/a.zip", "https://example.com/a.zip#fragment"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if policy.validate(u) == nil {
			t.Fatalf("invalid URL accepted: %s", raw)
		}
	}
	u, _ := url.Parse("https://corporate.example:8443/a.zip")
	policy.PrivateOrigins = []string{"https://corporate.example:8443"}
	if err := policy.validate(u); err != nil {
		t.Fatal(err)
	}
	u.Host = "other.example:8443"
	if policy.validate(u) == nil {
		t.Fatal("private authorization crossed origins")
	}
}

func TestFetchPackageZIPBlocksLoopback(t *testing.T) {
	_, _, err := FetchPackageZIP(context.Background(), "https://127.0.0.1/a.zip", PackageNetworkPolicy{}, PackageLimits{})
	if err == nil {
		t.Fatal("loopback accepted")
	}
}
