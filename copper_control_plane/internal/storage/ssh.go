package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Creds are live-OS SSH credentials from control-plane settings.
type Creds struct {
	Username string
	Password string
	Port     int // default 22
}

func (c Creds) Normalize() Creds {
	out := c
	out.Username = strings.TrimSpace(out.Username)
	if out.Port <= 0 {
		out.Port = 22
	}
	return out
}

func (c Creds) Validate() error {
	c = c.Normalize()
	if c.Username == "" {
		return fmt.Errorf("live OS SSH username is not set (Settings)")
	}
	if c.Password == "" {
		return fmt.Errorf("live OS SSH password is not set (Settings)")
	}
	return nil
}

// LineFunc receives each stdout/stderr line as it arrives (optional live progress).
type LineFunc func(line string)

// RunSSH executes command on host:port with password auth. Returns combined stdout+stderr.
func RunSSH(ctx context.Context, host string, creds Creds, command string) (string, error) {
	return runSSHOnce(ctx, host, creds, command, nil)
}

// RunSSHRetry retries transient SSH failures (empty success, dial/handshake drops,
// connection resets) which are common against live-OS sshd under batch load.
func RunSSHRetry(ctx context.Context, host string, creds Creds, command string, attempts int) (string, error) {
	return RunSSHRetryLines(ctx, host, creds, command, attempts, nil)
}

// RunSSHRetryLines is RunSSHRetry with an optional per-line callback for live progress
// (e.g. DOWNLOAD_PROGRESS while curling a cloud image). onLine may be nil.
func RunSSHRetryLines(ctx context.Context, host string, creds Creds, command string, attempts int, onLine LineFunc) (string, error) {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 5 {
		attempts = 5
	}
	var lastOut string
	var lastErr error
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastOut, lastErr
			}
			return "", err
		}
		out, err := runSSHOnce(ctx, host, creds, command, onLine)
		lastOut, lastErr = out, err
		if !sshTransient(out, err) {
			return out, err
		}
		if i == attempts {
			break
		}
		// Brief backoff before a fresh dial (new TCP + SSH session).
		delay := time.Duration(i) * 800 * time.Millisecond
		select {
		case <-ctx.Done():
			return lastOut, fmt.Errorf("%v (retry aborted: %w)", lastErr, ctx.Err())
		case <-time.After(delay):
		}
	}
	if lastErr != nil {
		return lastOut, fmt.Errorf("%w (after %d SSH attempts)", lastErr, attempts)
	}
	if strings.TrimSpace(lastOut) == "" {
		return lastOut, fmt.Errorf("empty SSH output after %d attempts (live-OS sshd flake)", attempts)
	}
	return lastOut, nil
}

// SudoBashLC builds `sudo -n bash -lc <script>`, preferring stdbuf -oL so progress
// lines flush over SSH pipes (bash fully-buffers stdout when not a TTY).
func SudoBashLC(script string) string {
	inner := "bash -lc " + ShellQuote(script)
	wrapper := "if command -v stdbuf >/dev/null 2>&1; then exec stdbuf -oL -eL " + inner + "; else exec " + inner + "; fi"
	return "sudo -n bash -lc " + ShellQuote(wrapper)
}

func sshTransient(out string, err error) bool {
	if err == nil {
		// Success with zero bytes is almost always a dropped/empty session, not a real script.
		return strings.TrimSpace(out) == ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "ssh dial"),
		strings.Contains(msg, "ssh handshake"),
		strings.Contains(msg, "ssh session"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "exceeded while awaiting"),
		strings.Contains(msg, "eof"),
		strings.Contains(msg, "handshake failed"),
		strings.Contains(msg, "unable to authenticate"),
		strings.Contains(msg, "empty ssh output"),
		strings.Contains(msg, "empty remote output"),
		strings.Contains(msg, "wait:"):
		return true
	default:
		return false
	}
}

func runSSHOnce(ctx context.Context, host string, creds Creds, command string, onLine LineFunc) (string, error) {
	creds = creds.Normalize()
	if err := creds.Validate(); err != nil {
		return "", err
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("empty data IP")
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("invalid data IP %q", host)
	}

	cfg := &ssh.ClientConfig{
		User: creds.Username,
		Auth: []ssh.AuthMethod{ssh.Password(creds.Password)},
		// Live OS in a DC lab; host keys rotate per boot / image.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(creds.Port))
	// TCP keepalive helps against silent middlebox / overloaded sshd drops.
	dialer := net.Dialer{
		Timeout:   20 * time.Second,
		KeepAlive: 15 * time.Second,
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w (is the live OS booted and reachable on the data network?)", addr, err)
	}
	// Do NOT SetDeadline(ctx.Deadline()) on the whole connection — that makes long
	// provision scripts (dd / cidata) fail with empty or truncated output under load.
	// Context cancel still aborts the session via the goroutine below.

	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	defer client.Close()

	// Application-level SSH keepalives while the remote command runs.
	keepaliveDone := make(chan struct{})
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-keepaliveDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_, _, _ = client.SendRequest("keepalive@openssh.com", true, nil)
			}
		}
	}()
	defer close(keepaliveDone)

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var buf bytes.Buffer
	outW := &lineCapture{buf: &buf, onLine: onLine}
	session.Stdout = outW
	session.Stderr = outW

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		// Drain briefly so we keep any partial output for diagnostics.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		outW.flush()
		out := buf.String()
		if strings.TrimSpace(out) == "" {
			return out, fmt.Errorf("ssh command timed out with empty output: %w", ctx.Err())
		}
		return out, fmt.Errorf("ssh command timed out: %w", ctx.Err())
	case err := <-done:
		outW.flush()
		out := buf.String()
		if err != nil {
			if err == io.EOF && strings.TrimSpace(out) == "" {
				return out, fmt.Errorf("ssh session EOF with empty output")
			}
			return out, fmt.Errorf("%w\n%s", err, strings.TrimSpace(out))
		}
		// Exit 0 with no bytes is valid (e.g. `true`, silent probes). Callers that
		// need output (provision scripts) use RunSSHRetry, which still treats
		// empty success as a flake worth retrying.
		return out, nil
	}
}

// lineCapture tees SSH output into a buffer and optionally emits complete lines live.
type lineCapture struct {
	buf     *bytes.Buffer
	pending []byte
	onLine  LineFunc
}

func (w *lineCapture) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.onLine == nil {
		return n, err
	}
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := string(w.pending[:i])
		w.pending = w.pending[i+1:]
		line = strings.TrimRight(line, "\r")
		if line != "" {
			w.onLine(line)
		}
	}
	return n, err
}

func (w *lineCapture) flush() {
	if w.onLine == nil || len(w.pending) == 0 {
		return
	}
	line := strings.TrimRight(string(w.pending), "\r")
	w.pending = nil
	if line != "" {
		w.onLine(line)
	}
}

// ShellQuote wraps s for a POSIX single-quoted argument.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
