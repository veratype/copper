package redfishx

import (
	"strings"
	"testing"
)

func TestPoliteInsertFailure(t *testing.T) {
	msg := politeInsertFailure("VirtualMedia3", true, []string{"image-only: 412:", "patch-resource: 412:"})
	if !strings.Contains(msg, "Could not attach media on VirtualMedia3") {
		t.Fatalf("missing polite lead-in: %s", msg)
	}
	if !strings.Contains(msg, "Eject") {
		t.Fatalf("expected eject guidance: %s", msg)
	}
	if !strings.Contains(msg, "SuperMicro") && !strings.Contains(msg, "precondition") {
		t.Fatalf("expected 412/precondition hint: %s", msg)
	}
}

func TestPoliteMountFailure(t *testing.T) {
	msg := politeMountFailure(3, 14, nil)
	if !strings.Contains(msg, "3 slot") || !strings.Contains(msg, "14 insert") {
		t.Fatalf("bad summary: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "eject") {
		t.Fatalf("expected eject guidance: %s", msg)
	}
}

func TestLooksPrecondition(t *testing.T) {
	if !looksPrecondition("412:") {
		t.Fatal("expected 412 to match")
	}
	if !looksPrecondition("PreconditionFailed") {
		t.Fatal("expected PreconditionFailed to match")
	}
	if looksPrecondition("400: bad request") {
		t.Fatal("400 should not look like precondition")
	}
}

func TestShortRedfishErr(t *testing.T) {
	if shortRedfishErr("") != "(no detail from BMC)" {
		t.Fatal("empty")
	}
	if shortRedfishErr("412:\nextra") != "412:" {
		t.Fatalf("got %q", shortRedfishErr("412:\nextra"))
	}
}
