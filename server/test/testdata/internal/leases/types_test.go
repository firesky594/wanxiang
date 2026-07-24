package leases

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLeaseTypesDefaultsAndStatuses(t *testing.T) {
	if LeaseTTL != 60*time.Second {
		t.Fatalf("LeaseTTL=%s", LeaseTTL)
	}
	if HeartbeatInterval != 15*time.Second {
		t.Fatalf("HeartbeatInterval=%s", HeartbeatInterval)
	}
	if ResumeWindow != 5*time.Minute {
		t.Fatalf("ResumeWindow=%s", ResumeWindow)
	}
	for _, status := range []LeaseStatus{LeaseActive, LeaseInterrupted, LeaseFrozen, LeaseExpired, LeaseRevoked} {
		if !status.Valid() {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if LeaseStatus("unknown").Valid() {
		t.Fatal("unknown status should be invalid")
	}
}

func TestLeaseTypesPublicViewHidesOtherAgentCredentials(t *testing.T) {
	lease := Lease{LeaseRef: LeaseRef{TaskID: 1, StepID: 2, AgentName: "agent-a", LeaseID: "secret-lease", LeaseVersion: 3}, Status: LeaseActive}
	otherJSON, err := json.Marshal(lease.PublicFor("agent-b"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(otherJSON), "secret-lease") || strings.Contains(string(otherJSON), "agent-a") {
		t.Fatalf("other agent view leaked lease credentials: %s", otherJSON)
	}
	ownerJSON, err := json.Marshal(lease.PublicFor("agent-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ownerJSON), "secret-lease") {
		t.Fatalf("owner view omitted lease id: %s", ownerJSON)
	}
}

func TestLeaseTypesFakeClockAdvancesDeterministically(t *testing.T) {
	start := time.Date(2026, 7, 15, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	clock := NewFakeClock(start)
	if got := clock.Now(); !got.Equal(start.UTC()) {
		t.Fatalf("Now=%s", got)
	}
	clock.Advance(LeaseTTL)
	if got := clock.Now(); !got.Equal(start.UTC().Add(LeaseTTL)) {
		t.Fatalf("advanced Now=%s", got)
	}
}
