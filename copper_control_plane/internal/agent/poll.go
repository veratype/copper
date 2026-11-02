package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"copper_control_plane/internal/store"
)

// PollBridge enqueues commands for a poll-mode agent and wakes waiters on result.
type PollBridge struct {
	Store *store.Store

	mu      sync.Mutex
	waiters map[string]chan pollWaitResult
}

type pollWaitResult struct {
	StatusCode int
	Body       []byte
	Err        string
}

func NewPollBridge(st *store.Store) *PollBridge {
	return &PollBridge{
		Store:   st,
		waiters: map[string]chan pollWaitResult{},
	}
}

// EnqueueAndWait stores a command and blocks until the agent posts a result or expiry.
func (p *PollBridge) EnqueueAndWait(method, path string, headers map[string]string, body []byte, wait time.Duration) (statusCode int, respBody []byte, err error) {
	if p == nil || p.Store == nil {
		return 0, nil, fmt.Errorf("poll bridge not configured")
	}
	if wait <= 0 {
		wait = 180 * time.Second
	}
	now := time.Now().UTC()
	id, err := newPollID()
	if err != nil {
		return 0, nil, err
	}
	hdrJSON := "{}"
	if headers != nil {
		b, mErr := json.Marshal(headers)
		if mErr != nil {
			return 0, nil, mErr
		}
		hdrJSON = string(b)
	}
	cmd := &store.AgentCommand{
		ID:          id,
		Method:      method,
		Path:        path,
		HeadersJSON: hdrJSON,
		Body:        body,
		Status:      store.AgentCmdQueued,
		QueuedAt:    now,
		ExpiresAt:   now.Add(wait),
	}
	ch := make(chan pollWaitResult, 1)
	p.mu.Lock()
	p.waiters[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
	}()

	if err := p.Store.EnqueueAgentCommand(cmd); err != nil {
		return 0, nil, fmt.Errorf("enqueue agent command: %w", err)
	}

	timer := time.NewTimer(wait + 2*time.Second)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.Err != "" && res.StatusCode == 0 {
			return 0, nil, fmt.Errorf("%s", res.Err)
		}
		return res.StatusCode, res.Body, nil
	case <-timer.C:
		// Prefer DB state if agent completed just as we timed out.
		if got, gErr := p.Store.GetAgentCommand(id); gErr == nil && got != nil {
			switch got.Status {
			case store.AgentCmdDone, store.AgentCmdError:
				if got.ResultError != "" && got.ResultStatus == 0 {
					return 0, nil, fmt.Errorf("%s", got.ResultError)
				}
				return got.ResultStatus, got.ResultBody, nil
			case store.AgentCmdExpired:
				return 0, nil, fmt.Errorf("agent did not complete command before %s (queued_at=%s)",
					got.ExpiresAt.Format(time.RFC3339), got.QueuedAt.Format(time.RFC3339))
			}
		}
		_ = p.Store.CompleteAgentCommand(id, 0, nil, "waiter timed out", time.Now().UTC())
		return 0, nil, fmt.Errorf("agent did not poll in time (queued at %s, waited %s)",
			now.Format(time.RFC3339), wait)
	}
}

func newPollID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// DeliverResult is called when the agent POSTs a command result.
func (p *PollBridge) DeliverResult(id string, statusCode int, body []byte, resultErr string) error {
	if p == nil || p.Store == nil {
		return fmt.Errorf("poll bridge not configured")
	}
	now := time.Now().UTC()
	if err := p.Store.CompleteAgentCommand(id, statusCode, body, resultErr, now); err != nil {
		return err
	}
	p.mu.Lock()
	ch := p.waiters[id]
	p.mu.Unlock()
	if ch != nil {
		select {
		case ch <- pollWaitResult{StatusCode: statusCode, Body: body, Err: resultErr}:
		default:
		}
	}
	return nil
}

// NotifyExpired wakes a waiter if the command expired in the DB.
func (p *PollBridge) NotifyExpired(id, msg string) {
	p.mu.Lock()
	ch := p.waiters[id]
	p.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- pollWaitResult{Err: msg}:
	default:
	}
}
