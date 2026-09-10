package core

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"sort"
	"strings"
)

var agentPackLimits = skills.PackageLimits{CompressedBytes: 100 << 20, ExpandedBytes: 100 << 20, FileBytes: 25 << 20, Entries: 5000}

type portableSkillVersion struct {
	SourceID   string                  `json:"source_id"`
	Ref        string                  `json:"ref"`
	Digest     string                  `json:"digest"`
	Provenance skills.SourceProvenance `json:"provenance"`
}
type agentSkillInventory struct {
	Schema   string                 `json:"schema"`
	Versions []portableSkillVersion `json:"versions"`
}

func agentSkillLockFiles(a *Agent) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, scope := range agentSkillScopes(a) {
		name := scope.Config.Lockfile
		if name != "skills.lock.json" && !(strings.HasPrefix(name, "skill-locks/") && strings.HasSuffix(name, ".json")) {
			return nil, errors.New("agent lockfile must be skills.lock.json or skill-locks/*.json")
		}
		if previous, ok := out[name]; ok && !bytes.Equal(previous, scope.Lock) {
			return nil, errors.New("different scope locks require different lockfile paths")
		}
		out[name] = scope.Lock
	}
	return out, nil
}

func exportAgentSkillFiles(ctx context.Context, a *Agent) ([]skills.PackageFile, error) {
	locks, err := agentSkillLockFiles(a)
	if err != nil {
		return nil, err
	}
	files := []skills.PackageFile{}
	for name, body := range locks {
		files = append(files, skills.PackageFile{Path: name, Content: body})
	}
	s, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	inventory := agentSkillInventory{Schema: "praimate.agent-skill-bundles/v1", Versions: []portableSkillVersion{}}
	seenVersions := map[string]bool{}
	seenObjects := map[string]bool{}
	var total int64
	for _, scope := range agentSkillScopes(a) {
		lock, err := skills.DecodeSkillVersionLock(scope.Lock)
		if err != nil {
			return nil, err
		}
		for _, e := range lock.Entries {
			key := e.SourceID + e.Digest
			if seenVersions[key] {
				continue
			}
			seenVersions[key] = true
			v, _, err := s.ReadVersion(ctx, e.Ref, e.Digest)
			if err != nil {
				return nil, err
			}
			if v.SourceID != e.SourceID {
				return nil, errors.New("agent lock identity mismatch")
			}
			inventory.Versions = append(inventory.Versions, portableSkillVersion{v.SourceID, v.Ref, v.Digest, v.Provenance()})
			if seenObjects[e.Digest] {
				continue
			}
			seenObjects[e.Digest] = true
			body, err := s.ExportPackageZIP(ctx, e.Ref, e.Digest)
			if err != nil {
				return nil, err
			}
			total += int64(len(body))
			if total > 100<<20 {
				return nil, errors.New("agent skill bundle limit exceeded")
			}
			files = append(files, skills.PackageFile{Path: "skills/" + e.Digest[7:] + ".zip", Content: body})
		}
	}
	sort.Slice(inventory.Versions, func(i, j int) bool {
		a, b := inventory.Versions[i], inventory.Versions[j]
		return a.SourceID+a.Digest < b.SourceID+b.Digest
	})
	body, err := json.Marshal(inventory)
	if err != nil {
		return nil, err
	}
	files = append(files, skills.PackageFile{Path: "skill-bundles.json", Content: body})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func readAgentPackFiles(ctx context.Context, name string) ([]skills.PackageFile, []byte, error) {
	body, err := skills.ReadLocalArchive(ctx, name, agentPackLimits.CompressedBytes)
	if err != nil {
		return nil, nil, err
	}
	files, err := skills.ReadPackageZIPFiles(ctx, bytes.NewReader(body), int64(len(body)), agentPackLimits)
	return files, body, err
}

func importAgentSkillFiles(ctx context.Context, a *Agent, files []skills.PackageFile, approveReviewed bool) error {
	locks, err := agentSkillLockFiles(a)
	if err != nil {
		return err
	}
	byName := map[string][]byte{}
	for _, file := range files {
		byName[file.Path] = file.Content
	}
	for name, want := range locks {
		if !bytes.Equal(byName[name], want) {
			return errors.New("pack lock does not match agent selection")
		}
	}
	var inventory agentSkillInventory
	raw := byName["skill-bundles.json"]
	if len(raw) > 4<<20 {
		return errors.New("skill inventory limit exceeded")
	}
	if err := skills.DecodePortableSkillRecord(raw, &inventory); err != nil {
		return err
	}
	if inventory.Schema != "praimate.agent-skill-bundles/v1" || len(inventory.Versions) > 1000 {
		return errors.New("invalid skill bundle inventory")
	}
	wanted := map[string]skills.SkillVersionLockEntry{}
	for _, scope := range agentSkillScopes(a) {
		l, err := skills.DecodeSkillVersionLock(scope.Lock)
		if err != nil {
			return err
		}
		for _, e := range l.Entries {
			key := e.SourceID + e.Digest
			if previous, ok := wanted[key]; ok && previous.Ref != e.Ref {
				return errors.New("conflicting skill alias")
			}
			wanted[key] = e
		}
	}
	selections := map[string]skills.SelectedPackage{}
	seen := map[string]bool{}
	allowed := map[string]bool{"agent.yaml": true, "runtime.json": true, "skill-bundles.json": true}
	for name := range locks {
		allowed[name] = true
	}
	var expanded int64
	entries := 0
	for _, v := range inventory.Versions {
		key := v.SourceID + v.Digest
		e, ok := wanted[key]
		if !ok || e.Ref != v.Ref || seen[key] {
			return errors.New("unlocked or duplicate skill inventory entry")
		}
		seen[key] = true
		if err := v.Provenance.Validate(); err != nil {
			return err
		}
		name := "skills/" + v.Digest[7:] + ".zip"
		allowed[name] = true
		if _, ok := selections[v.Digest]; ok {
			continue
		}
		body, ok := byName[name]
		if !ok {
			return errors.New("missing skill bundle")
		}
		candidates, _, err := skills.InspectPackageZIP(ctx, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		if len(candidates) != 1 || candidates[0].Subpath != "." || candidates[0].Digest != v.Digest {
			return errors.New("skill bundle digest mismatch")
		}
		selected, err := skills.SelectPackages(ctx, candidates, []skills.PackageSelection{{Candidate: 0, ExpectedDigest: v.Digest}}, skills.PackageLimits{})
		if err != nil {
			return err
		}
		for _, f := range selected[0].Files() {
			expanded += int64(len(f.Content))
			entries++
			if expanded > 100<<20 || entries > 5000 {
				return errors.New("aggregate nested skill bundle limit exceeded")
			}
		}
		selections[v.Digest] = selected[0]
	}
	if len(seen) != len(wanted) {
		return errors.New("pack is missing locked skills")
	}
	for name := range byName {
		if !allowed[name] && !strings.HasPrefix(name, "knowledge/") && !strings.HasPrefix(name, "requirements/") {
			return errors.New("unexpected v2 agent pack resource")
		}
	}
	s, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return err
	}
	defer s.Close()
	view, err := s.View(ctx)
	if err != nil {
		return err
	}
	_, err = s.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		for _, v := range inventory.Versions {
			version, err := tx.RegisterSource(ctx, v.SourceID, v.Ref, selections[v.Digest], v.Provenance)
			if err != nil {
				return err
			}
			if approveReviewed {
				if err := tx.Approve(ctx, version.Ref, version.Digest, true); err != nil {
					return err
				}
			}
		}
		// Validate provenance through the portable lock boundary. Approval, when
		// requested, came from a separate content-bound host review above; no
		// field inside the pack can request it.
		for i, scope := range agentSkillScopes(a) {
			if len(scope.Lock) == 0 {
				continue
			}
			l, err := skills.DecodeSkillVersionLock(scope.Lock)
			if err != nil {
				return err
			}
			if len(l.Entries) == 0 {
				continue
			}
			var versions []skills.SkillVersion
			for _, e := range l.Entries {
				v, _, err := tx.ResolveVersion(ctx, e.Ref, e.Digest)
				if err != nil {
					return err
				}
				versions = append(versions, v)
			}
			actual, err := tx.ExportLock(ctx, versions)
			if err != nil {
				return err
			}
			// Export sorts entries. Compare semantically without depending on author order.
			actualLock, err := skills.DecodeSkillVersionLock(actual)
			if err != nil {
				return err
			}
			sort.Slice(l.Entries, func(i, j int) bool { return l.Entries[i].Ref < l.Entries[j].Ref })
			x, _ := json.Marshal(l)
			y, _ := json.Marshal(actualLock)
			if !bytes.Equal(x, y) {
				return errors.New("bundle provenance does not match its lock")
			}
			_ = i
		}
		return nil
	})
	return err
}

func writeAgentSkillFiles(zw *zip.Writer, files []skills.PackageFile) error {
	for _, f := range files {
		w, err := zw.Create(f.Path)
		if err != nil {
			return err
		}
		if _, err = w.Write(f.Content); err != nil {
			return err
		}
	}
	return nil
}
