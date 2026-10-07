package main

import (
	"context"
	"testing"
)

func TestWorkflowCancellationIsScopedAndDuplicateIDsAreRejected(t *testing.T) {
	a := &App{ctx: context.Background()}
	first, finishFirst, err := a.beginWorkflowRun("first")
	if err != nil {
		t.Fatal(err)
	}
	defer finishFirst()
	second, finishSecond, err := a.beginWorkflowRun("second")
	if err != nil {
		t.Fatal(err)
	}
	defer finishSecond()
	if _, _, err := a.beginWorkflowRun("first"); err == nil {
		t.Fatal("duplicate execution accepted")
	}
	a.CancelWorkflowRun("first")
	if first.Err() != context.Canceled || second.Err() != nil {
		t.Fatal("cancellation crossed execution scopes")
	}
	a.CancelWorkflowRun("unknown")
	if second.Err() != nil {
		t.Fatal("unknown cancellation stopped another workflow")
	}
}
