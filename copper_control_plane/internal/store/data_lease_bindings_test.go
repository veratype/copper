package store

import (
	"path/filepath"
	"testing"
)

func TestDataLeaseBindingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.UpsertDataLeaseBinding("AA:BB:CC:DD:EE:01", DataLeaseBinding{
		ServerID: 7, ServerName: "box-a", IP: "10.0.0.5",
	}); err != nil {
		t.Fatal(err)
	}
	m, err := st.GetDataLeaseBindings()
	if err != nil {
		t.Fatal(err)
	}
	b, ok := m["aa:bb:cc:dd:ee:01"]
	if !ok || b.ServerID != 7 || b.ServerName != "box-a" {
		t.Fatalf("%+v", m)
	}
	if err := st.DeleteDataLeaseBinding("aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatal(err)
	}
	m, _ = st.GetDataLeaseBindings()
	if _, ok := m["aa:bb:cc:dd:ee:01"]; ok {
		t.Fatal("expected deleted")
	}
}
