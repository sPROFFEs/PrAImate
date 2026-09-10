package core

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSkillTaskBudgetSurvivesCoreReopenAndConcurrentAttempts(t *testing.T) {
	st := openTempStore(t)
	c, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.reserveSkillTaskInput(ctx, "task-1", 200, 50, 1000) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("accepted %d reservations; expected exactly five", accepted.Load())
	}
	reopened, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.reserveSkillTaskInput(ctx, "task-1", 1, 0, 10000); err == nil {
		t.Fatal("new Core/attempt widened or reset task limit")
	}
	raw, err := c.GetSetting(ctx, ScopeCLI, "skill_task_budget:"+skillReview("task-1"))
	var ledger skillTaskBudget
	if err != nil || json.Unmarshal(raw, &ledger) != nil || ledger.InputBytes != 1000 || ledger.SkillBytes != 250 {
		t.Fatal("bad durable counters", string(raw), err)
	}
	if err := reopened.reserveSkillTaskInput(ctx, "different-task", 100, 10, 1000); err != nil {
		t.Fatal("independent task contaminated", err)
	}
}
