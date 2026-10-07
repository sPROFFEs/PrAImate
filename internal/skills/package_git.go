package skills

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// GitPackageSource accepts a repository root with a separate ref/subpath, or
// a GitHub tree URL. Slash-containing refs in tree URLs require an explicit
// Ref so the branch/path boundary is not guessed. This adapter uses GitHub's
// documented REST API; other providers need their own adapter, not shell git.
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
	source, err := normalizeGitHubPackageSource(source)
	if err != nil {
		return GitPackageInspection{}, err
	}
	return fetchGitHubPackages(ctx, source, strings.Replace(source.Repository, "https://github.com/", "https://api.github.com/repos/", 1), policy, limits)
}

func normalizeGitHubPackageSource(source GitPackageSource) (GitPackageSource, error) {
	source.Repository = strings.TrimSpace(source.Repository)
	source.Ref = strings.TrimSpace(source.Ref)
	source.Subpath = strings.TrimSpace(source.Subpath)
	u, err := url.Parse(source.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return source, errors.New("expected a credential-free GitHub repository or tree URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return source, errors.New("expected a GitHub repository or tree URL")
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	for _, part := range parts[:2] {
		if err := validatePackagePath(part); err != nil || strings.Contains(part, "/") {
			return source, errors.New("invalid GitHub repository")
		}
	}
	if len(parts) > 2 {
		if len(parts) < 4 || parts[2] != "tree" || parts[3] == "" {
			return source, errors.New("expected a repository root or /tree/ref/path URL")
		}
		ref := source.Ref
		if ref == "" {
			ref = parts[3]
		}
		tree := strings.Join(parts[3:], "/")
		if tree != ref && !strings.HasPrefix(tree, ref+"/") {
			return source, errors.New("Git ref conflicts with tree URL; use a repository root with separate ref and subpath")
		}
		subpath := strings.TrimPrefix(strings.TrimPrefix(tree, ref), "/")
		if subpath != "" {
			if source.Subpath != "" && source.Subpath != subpath {
				return source, errors.New("Git subpath conflicts with tree URL")
			}
			source.Subpath = subpath
		}
		source.Ref = ref
	}
	if source.Subpath != "" && source.Subpath != "." {
		if err := validatePackagePath(source.Subpath); err != nil {
			return source, err
		}
	}
	source.Repository = "https://github.com/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	return source, nil
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
	limits, err := limits.normalized()
	if err != nil {
		return out, err
	}
	u, err := url.Parse(apiRepository + "/zipball/" + commit.SHA)
	if err != nil {
		return out, errors.New("invalid Git archive URL")
	}
	archive, err := fetchPackageBytes(ctx, u, policy, limits.CompressedBytes)
	if err != nil {
		return out, err
	}
	out.Candidates, out.Shared, err = inspectGitHubPackageArchive(ctx, archive, source.Subpath, limits)
	if err != nil {
		return out, err
	}
	out.Source = source
	out.ResolvedRevision = commit.SHA
	return out, nil
}

func inspectGitHubPackageArchive(ctx context.Context, archive []byte, subpath string, limits PackageLimits) ([]PackageCandidate, []string, error) {
	// Validate the complete bounded archive, then scope before parsing manifests
	// and assigning shared resources. Unrelated skills cannot affect the import.
	files, err := ReadPackageZIPFiles(ctx, bytes.NewReader(archive), int64(len(archive)), limits)
	if err != nil {
		return nil, nil, err
	}
	wrapper := ""
	var scoped []PackageFile
	for _, file := range files {
		first, name, ok := strings.Cut(file.Path, "/")
		if !ok {
			return nil, nil, errors.New("Git archive lacks repository wrapper")
		}
		if wrapper == "" {
			wrapper = first
		} else if first != wrapper {
			return nil, nil, errors.New("ambiguous Git archive wrapper")
		}
		if subpath == "" || subpath == "." || strings.HasPrefix(name, subpath+"/") {
			file.Path = name
			scoped = append(scoped, file)
		}
	}
	if len(scoped) == 0 {
		return nil, nil, errors.New("no skills at requested Git subpath")
	}
	return inspectPackageFiles(ctx, scoped)
}
