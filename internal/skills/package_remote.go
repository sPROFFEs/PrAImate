package skills

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// PackageNetworkPolicy belongs to the host. PrivateOrigins are exact HTTPS
// origins explicitly approved by the user, not values imported from packages.
type PackageNetworkPolicy struct {
	PrivateOrigins []string
	// Additional host-managed certificate roots, e.g. a corporate Gitea CA.
	RootCAs *x509.CertPool
}

func (p PackageNetworkPolicy) validate(u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("package source requires HTTPS without credentials or fragment")
	}
	if u.Port() != "" && u.Port() != "443" && !p.private(u) {
		return errors.New("package source port not approved")
	}
	return nil
}

func (p PackageNetworkPolicy) private(u *url.URL) bool {
	for _, origin := range p.PrivateOrigins {
		if origin == "https://"+u.Host {
			return true
		}
	}
	return false
}

func publicPackageIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Reserved/special-use ranges must not be treated as public destinations.
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "2002::/16"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}

// FetchPackageZIP downloads a bounded snapshot, then uses the common inspector.
// It disables environment proxies and pins each connection to a checked DNS
// result. Redirects are revalidated; TLS certificate verification stays on.
func FetchPackageZIP(ctx context.Context, rawURL string, policy PackageNetworkPolicy, limits PackageLimits) ([]PackageCandidate, []string, error) {
	limits, err := limits.normalized()
	if err != nil {
		return nil, nil, err
	}
	if limits.CompressedBytes == 1<<63-1 {
		return nil, nil, errors.New("invalid compressed limit")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, errors.New("invalid package URL")
	}
	if err := policy.validate(u); err != nil {
		return nil, nil, err
	}
	body, err := fetchPackageBytes(ctx, u, policy, limits.CompressedBytes)
	if err != nil {
		return nil, nil, err
	}
	return InspectPackageZIPWithLimits(ctx, bytes.NewReader(body), int64(len(body)), limits)
}

func fetchPackageBytes(ctx context.Context, u *url.URL, policy PackageNetworkPolicy, limit int64) ([]byte, error) {
	if limit < 1 || limit == 1<<63-1 {
		return nil, errors.New("invalid download limit")
	}
	if err := policy.validate(u); err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: policy.RootCAs}}
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		origin := &url.URL{Scheme: "https", Host: address}
		if port == "443" {
			origin.Host = host
			if strings.Contains(host, ":") {
				origin.Host = "[" + host + "]"
			}
		}
		allowed := policy.private(origin) || policy.private(&url.URL{Scheme: "https", Host: address})
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("package DNS resolution failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("package DNS returned no addresses")
		}
		for _, ip := range ips {
			if !allowed && !publicPackageIP(ip) {
				return nil, errors.New("package destination blocked by network policy")
			}
		}
		var dialer net.Dialer
		dialer.Timeout = 10 * time.Second
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("package redirect limit exceeded")
		}
		return policy.validate(req.URL)
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid package request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("package download failed or destination denied")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("package download returned non-success status")
	}
	if resp.ContentLength > limit {
		return nil, errors.New("package download limit exceeded")
	}
	body, err := io.ReadAll(io.LimitReader(packageContextReader{ctx, resp.Body}, limit+1))
	if err != nil {
		return nil, errors.New("package body read failed")
	}
	if int64(len(body)) > limit {
		return nil, errors.New("package download limit exceeded")
	}
	return body, nil
}
