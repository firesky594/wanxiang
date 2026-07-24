package main

import "testing"

func TestWorkerModeAcceptsOnlyInternalFDMode(t *testing.T) {
	fd, worker, err := workerMode([]string{"agent-worker", "--input-fd", "3"})
	if err != nil || !worker || fd != 3 {
		t.Fatalf("fd=%d worker=%v err=%v", fd, worker, err)
	}
	for _, args := range [][]string{{"agent-worker"}, {"agent-worker", "--input-fd", "2"}, {"agent-worker", "--input-fd", "3", "codex"}} {
		if _, worker, err := workerMode(args); !worker || err == nil {
			t.Fatalf("args=%v worker=%v err=%v", args, worker, err)
		}
	}
}
func TestWorkerModeLeavesServerModeUntouched(t *testing.T) {
	if _, worker, err := workerMode(nil); worker || err != nil {
		t.Fatalf("worker=%v err=%v", worker, err)
	}
}
