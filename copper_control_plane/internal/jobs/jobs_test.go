package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memPersist struct {
	mu   sync.Mutex
	jobs map[string]Job
}

func (m *memPersist) UpsertJob(j Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs == nil {
		m.jobs = map[string]Job{}
	}
	m.jobs[j.ID] = j
	return nil
}

func (m *memPersist) GetJob(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, nil
	}
	cp := j
	return &cp, nil
}

func (m *memPersist) ListJobs(f ListFilter) ([]*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for _, j := range m.jobs {
		cp := j
		out = append(out, &cp)
	}
	return out, nil
}

func (m *memPersist) FailInterruptedJobs(reason string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, j := range m.jobs {
		if j.Status == StatusQueued || j.Status == StatusRunning {
			j.Status = StatusError
			j.Phase = "error"
			j.Error = reason
			j.FinishedAt = time.Now().UTC()
			m.jobs[id] = j
			n++
		}
	}
	return n, nil
}

func TestEnqueueAndComplete(t *testing.T) {
	p := &memPersist{}
	reg := New(p, nil)
	reg.SetExecutors(2)
	done := make(chan struct{})
	reg.Register("test.echo", func(ctx context.Context, job *Job, progress Progress) error {
		progress("work", "hello")
		reg.SetResult(job.ID, map[string]string{"msg": "ok"})
		close(done)
		return nil
	})

	job, err := reg.Enqueue(EnqueueOpts{Type: "test.echo", ServerName: "s1", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued && job.Status != StatusRunning {
		t.Fatalf("unexpected status %s", job.Status)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for handler")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := reg.Get(job.ID)
		if !ok {
			t.Fatal("job missing")
		}
		if got.Status == StatusOK {
			if got.Phase != "done" {
				t.Fatalf("phase=%s", got.Phase)
			}
			if len(got.Log) == 0 {
				t.Fatal("expected log lines")
			}
			persisted, err := p.GetJob(job.ID)
			if err != nil || persisted == nil {
				t.Fatalf("persist: %v %#v", err, persisted)
			}
			if persisted.Status != StatusOK {
				t.Fatalf("persisted status %s", persisted.Status)
			}
			return
		}
		if got.Status == StatusError {
			t.Fatalf("job error: %s", got.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func TestFailInterruptedOnStartup(t *testing.T) {
	p := &memPersist{jobs: map[string]Job{
		"abc": {ID: "abc", Type: "x", Status: StatusRunning, Phase: "work", Log: []string{}},
	}}
	_ = New(p, nil)
	got, err := p.GetJob("abc")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusError {
		t.Fatalf("want error, got %s", got.Status)
	}
}

func TestSameServerSerializes(t *testing.T) {
	reg := New(nil, nil)
	reg.SetExecutors(4)

	var mu sync.Mutex
	running := 0
	maxRunning := 0
	gate := make(chan struct{})
	started := make(chan struct{}, 2)

	reg.Register("test.work", func(ctx context.Context, job *Job, progress Progress) error {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		mu.Unlock()
		started <- struct{}{}

		select {
		case <-gate:
		case <-ctx.Done():
			mu.Lock()
			running--
			mu.Unlock()
			return ctx.Err()
		}

		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})

	j1, err := reg.Enqueue(EnqueueOpts{Type: "test.work", ServerID: 1, ServerName: "s1", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first job did not start")
	}

	j2, err := reg.Enqueue(EnqueueOpts{Type: "test.work", ServerID: 1, ServerName: "s1", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	// Second job must stay queued while first holds the server.
	time.Sleep(150 * time.Millisecond)
	got2, ok := reg.Get(j2.ID)
	if !ok {
		t.Fatal("job2 missing")
	}
	if got2.Status != StatusQueued {
		t.Fatalf("job2 status=%s want queued (same-server serialize)", got2.Status)
	}
	mu.Lock()
	if maxRunning != 1 || running != 1 {
		mu.Unlock()
		t.Fatalf("running=%d max=%d want only one on same server", running, maxRunning)
	}
	mu.Unlock()

	close(gate)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, _ := reg.Get(j1.ID)
		b, _ := reg.Get(j2.ID)
		if a != nil && b != nil && a.Status == StatusOK && b.Status == StatusOK {
			mu.Lock()
			mr := maxRunning
			mu.Unlock()
			if mr != 1 {
				t.Fatalf("max concurrent on same server=%d want 1", mr)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("jobs did not complete")
}

func TestWithServerSerializesWithJobs(t *testing.T) {
	reg := New(nil, nil)
	reg.SetExecutors(4)

	jobHold := make(chan struct{})
	jobStarted := make(chan struct{})
	reg.Register("test.work", func(ctx context.Context, job *Job, progress Progress) error {
		close(jobStarted)
		select {
		case <-jobHold:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})

	if _, err := reg.Enqueue(EnqueueOpts{Type: "test.work", ServerID: 7, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}

	syncEntered := make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		syncDone <- reg.WithServer(context.Background(), 7, func(ctx context.Context) error {
			close(syncEntered)
			return nil
		})
	}()

	select {
	case <-syncEntered:
		t.Fatal("WithServer ran while job held the server")
	case <-time.After(150 * time.Millisecond):
	}

	close(jobHold)
	select {
	case <-syncEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("WithServer did not run after job released")
	}
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WithServer did not finish")
	}
}

func TestDifferentServersParallel(t *testing.T) {
	reg := New(nil, nil)
	reg.SetExecutors(4)

	var mu sync.Mutex
	running := 0
	maxRunning := 0
	bothStarted := make(chan struct{})
	release := make(chan struct{})

	reg.Register("test.work", func(ctx context.Context, job *Job, progress Progress) error {
		mu.Lock()
		running++
		if running > maxRunning {
			maxRunning = running
		}
		if running == 2 {
			select {
			case <-bothStarted:
			default:
				close(bothStarted)
			}
		}
		mu.Unlock()

		select {
		case <-release:
		case <-ctx.Done():
			mu.Lock()
			running--
			mu.Unlock()
			return ctx.Err()
		}
		mu.Lock()
		running--
		mu.Unlock()
		return nil
	})

	if _, err := reg.Enqueue(EnqueueOpts{Type: "test.work", ServerID: 1, ServerName: "s1", Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Enqueue(EnqueueOpts{Type: "test.work", ServerID: 2, ServerName: "s2", Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-bothStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("expected both servers to run in parallel")
	}
	mu.Lock()
	mr := maxRunning
	mu.Unlock()
	if mr < 2 {
		t.Fatalf("max concurrent=%d want >= 2 across different servers", mr)
	}
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list := reg.List(ListFilter{Limit: 10})
		ok := len(list) >= 2
		for _, j := range list {
			if j.Status != StatusOK {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("jobs did not complete")
}

func TestListSortedNewestFirst(t *testing.T) {
	reg := New(nil, nil)
	reg.Register("test.noop", func(ctx context.Context, job *Job, progress Progress) error {
		return nil
	})
	older := &Job{
		ID: "a-older", Type: "test.noop", Status: StatusOK,
		CreatedAt: time.Now().UTC().Add(-time.Hour), Log: []string{},
	}
	newer := &Job{
		ID: "b-newer", Type: "test.noop", Status: StatusOK,
		CreatedAt: time.Now().UTC(), Log: []string{},
	}
	reg.mu.Lock()
	reg.jobs[older.ID] = older
	reg.jobs[newer.ID] = newer
	reg.mu.Unlock()

	list := reg.List(ListFilter{Limit: 10})
	if len(list) != 2 {
		t.Fatalf("len=%d want 2", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != older.ID {
		t.Fatalf("order=%v,%v want newer then older", list[0].ID, list[1].ID)
	}
}

func TestBusyServers(t *testing.T) {
	reg := New(nil, nil)
	reg.mu.Lock()
	reg.serverBusy[7] = "job-abc"
	reg.mu.Unlock()
	if !reg.ServerBusy(7) {
		t.Fatal("expected busy")
	}
	m := reg.BusyServers()
	if m[7] != "job-abc" {
		t.Fatalf("busy map=%v", m)
	}
	m[7] = "mutated"
	if reg.BusyServers()[7] != "job-abc" {
		t.Fatal("BusyServers should return a copy")
	}
}
