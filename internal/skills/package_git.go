package skills

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// GitPackageSource separates a repository root, a complete ref (including
// slashes), and a portable subpath. Ambiguous web /tree/ref/path URLs are not
// guessed. This first network adapter supports GitHub's documented REST API;
// other forge providers must supply their own verified adapter, not shell git.
type GitPackageSource struct {
	Repository string
	Ref        string
	Subpath    string
}

type GitPackageInspection struct {
	Source           GitPackageSource
	ResolvedRevision string
	Candidates       []PackageCandidate
	Shared           []string
}

func FetchGitHubPackages(ctx context.Context, source GitPackageSource, policy PackageNetworkPolicy, limits PackageLimits) (GitPackageInspection, error) {
	u, err := url.Parse(source.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return GitPackageInspection{}, errors.New("expected a credential-free GitHub repository root")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return GitPackageInspection{}, errors.New("supply repository, ref and subpath separately")
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	for _, part := range parts {
		if err := validatePackagePath(part); err != nil || strings.Contains(part, "/") {
			return GitPackageInspection{}, errors.New("invalid GitHub repository")
		}
	}
	source.Repository = "https://github.com/" + strings.Join(parts, "/")
	return fetchGitHubPackages(ctx, source, "https://api.github.com/repos/"+url.PathEscape(parts[0])+"/"+url.PathEscape(parts[1]), policy, limits)
}

func fetchGitHubPackages(ctx context.Context, source GitPackageSource, apiRepository string, policy PackageNetworkPolicy, limits PackageLimits) (GitPackageInspection, error) {
	var out GitPackageInspection
	if source.Subpath != "" && source.Subpath != "." {
		if err := validatePackagePath(source.Subpath); err != nil {
			return out, err
		}
	}
	get := func(endpoint string, dst any) error {
		u, err := url.Parse(endpoint)
		if err != nil {
			return errors.New("invalid Git API endpoint")
		}
		data, err := fetchPackageBytes(ctx, u, policy, 1<<20)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, dst)
	}
	ref := source.Ref
	if ref == "" {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := get(apiRepository, &repo); err != nil {
			return out, err
		}
		ref = repo.DefaultBranch
	}
	if ref == "" || len(ref) > 1024 || strings.ContainsAny(ref, "\x00\r\n") {
		return out, errors.New("invalid Git ref")
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := get(apiRepository+"/commits/"+url.PathEscape(ref), &commit); err != nil {
		return out, err
	}
	if len(commit.SHA) != 40 || strings.ToLower(commit.SHA) != commit.SHA {
		return out, errors.New("Git API did not return a full revision")
	}
	if _, err := hex.DecodeString(commit.SHA); err != nil {
		return out, errors.New("invalid Git revision")
	}
	candidates, shared, err := FetchPackageZIP(ctx, apiRepository+"/zipball/"+commit.SHA, policy, limits)
	if err != nil {
		return out, err
	}
	// GitHub archives have one generated top directory; retain repository-relative
	// paths rather than letting that unstable wrapper become source identity.
	wrapper := ""
	for _, c := range candidates {
		if c.Subpath == "." {
			return out, errors.New("Git archive lacks repository wrapper")
		}
		first := strings.SplitN(c.Subpath, "/", 2)[0]
		if wrapper == "" {
			wrapper = first
		} else if wrapper != first {
			return out, errors.New("ambiguous Git archive wrapper")
		}
	}
	strip := func(name string) string {
		if name == wrapper {
			return "."
		}
		return strings.TrimPrefix(name, wrapper+"/")
	}
	for i, name := range shared {
		if !strings.HasPrefix(name, wrapper+"/") {
			return out, errors.New("shared resource outside Git archive wrapper")
		}
		shared[i] = strip(name)
	}
	resources := make(map[string]PackageFile)
	if len(candidates) > 0 {
		for name, file := range candidates[0].shared {
			file.Path = strip(name)
			resources[file.Path] = file
		}
	}
	for _, c := range candidates {
		c.Subpath = strip(c.Subpath)
		c.shared = resources
		if source.Subpath == "" || source.Subpath == "." || c.Subpath == source.Subpath || strings.HasPrefix(c.Subpath, source.Subpath+"/") {
			out.Candidates = append(out.Candidates, c)
		}
	}
	if len(out.Candidates) == 0 {
		return GitPackageInspection{}, errors.New("no skills at requested Git subpath")
	}
	out.Source = source
	out.ResolvedRevision = commit.SHA
	out.Shared = shared
	return out, nil
}
