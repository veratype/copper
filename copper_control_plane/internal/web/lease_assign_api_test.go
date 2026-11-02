package web

import (
	"strings"
	"testing"
)

func TestMergeManualMACIntoInventory(t *testing.T) {
	payload := `{"network":[{"mac_address":"aa:bb:cc:dd:ee:01"}],"counts":{"network":1}}`
	added, out, err := mergeManualMACIntoInventory(payload, "11:22:33:44:55:66", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added=%d", added)
	}
	if !strings.Contains(out, "11:22:33:44:55:66") || !strings.Contains(out, `"source": "manual"`) {
		t.Fatalf("%s", out)
	}
	added2, _, err := mergeManualMACIntoInventory(out, "11:22:33:44:55:66", "manual")
	if err != nil || added2 != 0 {
		t.Fatalf("dup added=%d err=%v", added2, err)
	}
}
