package web

import (
	"encoding/json"
	"testing"
)

func TestSummarizeEject(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"ok": true,
		"ejected": []map[string]any{
			{"id": "1", "name": "iLO Virtual CD-ROM", "was_inserted": true, "ejected": true, "method": "eject-action"},
			{"id": "2", "name": "Floppy", "was_inserted": false, "ejected": true, "method": "already-empty"},
		},
	})
	got := summarizeEject(raw)
	want := "iLO Virtual CD-ROM:ejected(eject-action), Floppy:empty"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
