package executor

import "testing"

func TestExecutorTypesRejectUnknownStatusAndAction(t *testing.T) {
	for _, status := range []RunStatus{RunStarting, RunRunning, RunCheckpointed, RunCompleted, RunInterrupted, RunFailed, RunStopped} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if RunStatus("unknown").Valid() {
		t.Fatal("unknown run status accepted")
	}
	for _, action := range []ActionType{ActionReadFile, ActionWriteFile, ActionRunCheck, ActionGitStatus, ActionCheckpoint} {
		if !action.Valid() {
			t.Fatalf("action %q should be valid", action)
		}
	}
	if ActionType("shell").Valid() {
		t.Fatal("shell action accepted")
	}
}
