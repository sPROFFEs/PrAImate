package skills

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHostReadWaitsForConcurrentTransaction(t *testing.T) {
	directory := t.TempDir()
	writer, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenHostSkillStore(directory, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := writer.Update(context.Background(), 0, func(*SkillHostTransaction) error {
			close(locked)
			<-release
			return nil
		})
		done <- err
	}()
	<-locked
	read := make(chan error, 1)
	go func() {
		view, err := reader.View(context.Background())
		if err == nil && view.Revision != 1 {
			err = errors.New("reader observed a stale transaction")
		}
		read <- err
	}()
	select {
	case err := <-read:
		close(release)
		<-done
		t.Fatalf("read failed instead of awaiting contention: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
}

func TestHostContendedReadHonorsCancellation(t *testing.T) {
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Update(context.Background(), 0, func(*SkillHostTransaction) error {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := s.View(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock ignored cancellation: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
