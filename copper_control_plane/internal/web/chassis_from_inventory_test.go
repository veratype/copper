package web

import (
	"path/filepath"
	"testing"

	"copper_control_plane/internal/store"
)

func TestApplyChassisIDFromInventoryFillsEmpty(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateServer(store.Server{
		Name: "sm", Endpoint: "https://10.0.0.1", Username: "ADMIN", Password: "x",
		Status: store.StatusBound, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	payload := `{"system":{"manufacturer":"Supermicro","model":"SYS-621C-TN12R","serial_number":"E497867X4901901"}}`
	changed, err := s.applyChassisIDFromInventory(srv, payload)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChassisID != "E497867X4901901" {
		t.Fatalf("chassis_id=%q", got.ChassisID)
	}
}

func TestApplyChassisIDFromInventoryKeepsExisting(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateServer(store.Server{
		Name: "sm", Endpoint: "https://10.0.0.1", Username: "ADMIN", Password: "x",
		ChassisID: "ALREADY", Status: store.StatusBound, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	changed, err := s.applyChassisIDFromInventory(srv, `{"system":{"serial_number":"E497867X4901901"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("must not overwrite existing chassis_id")
	}
	got, _ := st.GetServer(id)
	if got.ChassisID != "ALREADY" {
		t.Fatalf("got %q", got.ChassisID)
	}
}
