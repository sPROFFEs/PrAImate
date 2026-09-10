package skills

import "context"

// ReadDraft returns an independent editable snapshot without creating history.
func (s *HostSkillStore) ReadDraft(ctx context.Context, key string) (*SkillDraft, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return nil, err
	}
	defer func() { tx.active = false }()
	return tx.LoadDraft(ctx, key)
}

func (s *HostSkillStore) VersionApproved(ctx context.Context, ref, digest string) (bool, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return false, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return false, err
	}
	defer func() { tx.active = false }()
	_, approved, err := tx.ResolveVersion(ctx, ref, digest)
	return approved, err
}
