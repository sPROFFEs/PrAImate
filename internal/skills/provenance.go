package skills

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// SourceProvenance is descriptive host-reviewed origin metadata, never trust.
// A fork has its own SourceID and records the original source/digest explicitly.
type SourceProvenance struct {
	Kind             string `json:"kind,omitempty"`
	Origin           string `json:"origin,omitempty"`
	Subpath          string `json:"subpath,omitempty"`
	ResolvedRevision string `json:"resolved_revision,omitempty"`
	DerivedSource    string `json:"derived_source,omitempty"`
	DerivedDigest    string `json:"derived_digest,omitempty"`
}

func (v SkillVersion) Provenance() SourceProvenance {
	return SourceProvenance{v.SourceKind, v.Origin, v.Subpath, v.ResolvedRevision, v.DerivedSource, v.DerivedDigest}
}

// A repository may advance to a new commit without changing package bytes.
// That observation must not replace the original immutable version receipt.
func sameSourceProvenance(a, b SourceProvenance) bool {
	a.ResolvedRevision = ""
	b.ResolvedRevision = ""
	return a == b
}

func (p SourceProvenance) Validate() error {
	if p.Kind != "" && p.Kind != "own" && p.Kind != "builtin" && p.Kind != "external" && p.Kind != "legacy-user" {
		return errors.New("unknown skill source kind")
	}
	if p.Origin != "" {
		u, err := url.Parse(p.Origin)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(p.Origin) > 2048 {
			return errors.New("origin must be a credential-free canonical HTTPS URL")
		}
	}
	if p.Kind == "external" && p.Origin == "" {
		return errors.New("external skill requires origin")
	}
	if p.Subpath != "" && p.Subpath != "." {
		if err := validatePackagePath(p.Subpath); err != nil {
			return err
		}
	}
	if p.ResolvedRevision != "" {
		if len(p.ResolvedRevision) != 40 || strings.ToLower(p.ResolvedRevision) != p.ResolvedRevision {
			return errors.New("source revision must be a full commit")
		}
		if _, err := hex.DecodeString(p.ResolvedRevision); err != nil {
			return errors.New("invalid source revision")
		}
	}
	if (p.DerivedSource == "") != (p.DerivedDigest == "") {
		return errors.New("fork requires both source and digest")
	}
	if p.DerivedSource != "" {
		if err := validateRegistryIdentity(p.DerivedSource, "local/fork"); err != nil {
			return err
		}
		if len(p.DerivedDigest) != 71 || !strings.HasPrefix(p.DerivedDigest, "sha256:") || strings.ToLower(p.DerivedDigest) != p.DerivedDigest {
			return errors.New("invalid fork digest")
		}
		if _, err := hex.DecodeString(p.DerivedDigest[7:]); err != nil {
			return err
		}
	}
	return nil
}

func NewOwnSkillSourceID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return "own:" + hex.EncodeToString(id[:]), nil
}

func ExternalSkillSourceID(origin, subpath string) (string, error) {
	p := SourceProvenance{Kind: "external", Origin: origin, Subpath: subpath}
	if err := p.Validate(); err != nil {
		return "", err
	}
	u, _ := url.Parse(origin)
	u.Host = strings.ToLower(u.Host)
	if subpath == "." {
		subpath = ""
	}
	hash := sha256.Sum256([]byte("praimate-external-source-v1\x00" + u.String() + "\x00" + subpath))
	return "external:" + hex.EncodeToString(hash[:]), nil
}
