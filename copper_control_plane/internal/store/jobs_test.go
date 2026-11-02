package store

import (
	"path/filepath"
	"testing"
	"time"

	"copper_control_plane/internal/jobs"
)

func TestJobPersistenceAndInterrupt(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := jobs.Job{
		ID: "j1", Type: jobs.TypeStorageDetect, ServerID: 7, ServerName: "node-a",
		Status: jobs.StatusRunning, Phase: "detect", Log: []string{"[start] go"},
		CreatedAt: time.Now().UTC(), StartedAt: time.Now().UTC(),
	}
	if err := st.UpsertJob(j); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob("j1")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.ServerName != "node-a" || got.Status != jobs.StatusRunning {
		t.Fatalf("unexpected %#v", got)
	}

	n, err := st.FailInterruptedJobs("restart")
	if err != nil || n != 1 {
		t.Fatalf("interrupt n=%d err=%v", n, err)
	}
	got, _ = st.GetJob("j1")
	if got.Status != jobs.StatusError || got.Error != "restart" {
		t.Fatalf("after interrupt %#v", got)
	}

	list, err := st.ListJobs(jobs.ListFilter{ServerID: 7, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %#v", err, list)
	}
}
