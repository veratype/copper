package storage

import (
	"bytes"
	"errors"
	"testing"
)

func TestSSHTransientEmptySuccess(t *testing.T) {
	if !sshTransient("", nil) {
		t.Fatal("empty success should be transient")
	}
	if sshTransient("CIDATA_OK", nil) {
		t.Fatal("real output should not be transient")
	}
}

func TestSSHTransientDialErrors(t *testing.T) {
	cases := []string{
		"ssh dial 10.0.0.1:22: connection refused",
		"ssh handshake 10.0.0.1:22: EOF",
		"connection reset by peer",
		"empty SSH output (remote produced no stdout/stderr)",
	}
	for _, msg := range cases {
		if !sshTransient("", errors.New(msg)) {
			t.Fatalf("expected transient: %s", msg)
		}
	}
	if sshTransient("oops", errors.New("command not allowed: foo")) {
		t.Fatal("logical errors should not retry")
	}
}

func TestLineCapture_EmitsCompleteLines(t *testing.T) {
	var got []string
	var buf bytes.Buffer
	w := &lineCapture{buf: &buf, onLine: func(line string) { got = append(got, line) }}
	_, _ = w.Write([]byte("DOWNLOAD_PROGRESS pct=10\nDOWNLOAD_PROGRESS pct=20\npartial"))
	if len(got) != 2 || got[0] != "DOWNLOAD_PROGRESS pct=10" || got[1] != "DOWNLOAD_PROGRESS pct=20" {
		t.Fatalf("got %#v", got)
	}
	w.flush()
	if len(got) != 3 || got[2] != "partial" {
		t.Fatalf("after flush %#v", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("partial")) {
		t.Fatal("buffer should keep full output")
	}
}

func TestSudoBashLC_PrefersStdbuf(t *testing.T) {
	cmd := SudoBashLC("echo hi")
	if !containsAll(cmd, "sudo -n bash -lc", "stdbuf -oL -eL", "echo hi") {
		t.Fatalf("unexpected: %s", cmd)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !bytes.Contains([]byte(s), []byte(p)) {
			return false
		}
	}
	return true
}
