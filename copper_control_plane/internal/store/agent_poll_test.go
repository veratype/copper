package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestClaimNextAgentCommandRestartsExpiry(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "poll.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	queued := time.Now().UTC().Add(-2 * time.Minute)
	origExpiry := queued.Add(3 * time.Minute) // 1 minute left if we didn't refresh
	cmd := &AgentCommand{
		ID:        "cmd1",
		Method:    "GET",
		Path:      "/v1/system",
		Status:    AgentCmdQueued,
		QueuedAt:  queued,
		ExpiresAt: origExpiry,
	}
	if err := st.EnqueueAgentCommand(cmd); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	got, err := st.ClaimNextAgentCommand(now)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected command")
	}
	if got.Status != AgentCmdDelivered {
		t.Fatalf("status=%s", got.Status)
	}
	if !got.DeliveredAt.Equal(now) && got.DeliveredAt.Before(now.Add(-time.Second)) {
		t.Fatalf("delivered_at=%v want ~%v", got.DeliveredAt, now)
	}
	// Full 3m window from delivery, not the 1m that remained on the queue clock.
	wantMin := now.Add(2*time.Minute + 50*time.Second)
	if got.ExpiresAt.Before(wantMin) {
		t.Fatalf("expires_at=%v want at least %v (refreshed from delivery)", got.ExpiresAt, wantMin)
	}
}
