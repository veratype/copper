package store

import (
	"path/filepath"
	"testing"
)

func TestSummarizeInventory_DedupesAndBuckets(t *testing.T) {
	payload := `{
  "cpu": [
    {"id":"CPU1","model":"Xeon Gold 6330","total_cores":28,"total_threads":56},
    {"id":"CPU1","model":"Xeon Gold 6330","total_cores":28,"total_threads":56},
    {"id":"CPU2","model":"Xeon Gold 6330","total_cores":28,"total_threads":56}
  ],
  "ram": [
    {"id":"DIMM1","capacity_mib":32768},
    {"id":"DIMM2","capacity_mib":32768},
    {"id":"DIMM1","capacity_mib":32768}
  ],
  "gpu": [
    {"id":"GPU1","model":"NVIDIA A100","serial_number":"SN-A"},
    {"id":"GPU1","model":"NVIDIA A100","serial_number":"SN-A"},
    {"id":"GPU2","model":"NVIDIA A100","serial_number":"SN-B"}
  ],
  "network": [
    {"id":"eth0","mac_address":"AA:BB:CC:DD:EE:01"},
    {"id":"eth0-dup","mac_address":"aa-bb-cc-dd-ee-01"},
    {"id":"eth1","mac_address":"AA:BB:CC:DD:EE:02"},
    {"id":"bmc0","mac_address":"11:22:33:44:55:66"}
  ],
  "bmc": [
    {"id":"BMC","mac_addresses":["11:22:33:44:55:66"]}
  ],
  "psu": [
    {"id":"PSU1","serial_number":"P1"},
    {"id":"PSU1","serial_number":"P1"},
    {"id":"PSU2","serial_number":"P2"}
  ],
  "storage": [
    {"id":"d1","serial_number":"H1","media_type":"HDD","protocol":"SAS","capacity_gib":1800},
    {"id":"d1-dup","serial_number":"H1","media_type":"HDD","protocol":"SAS","capacity_gib":1800},
    {"id":"d2","serial_number":"H2","media_type":"HDD","protocol":"SAS","capacity_gib":1800},
    {"id":"n1","serial_number":"N1","media_type":"SSD","protocol":"NVMe","capacity_gib":1920},
    {"id":"s1","serial_number":"S1","media_type":"SSD","protocol":"SATA","capacity_gib":480}
  ]
}`
	facts, err := SummarizeInventory(payload)
	if err != nil {
		t.Fatal(err)
	}
	if facts.CPUCount != 2 || facts.CPUCores != 56 || facts.CPUThreads != 112 {
		t.Fatalf("cpu: %+v", facts)
	}
	if facts.RAMModules != 2 || facts.RAMGiB != 64 {
		t.Fatalf("ram: %+v", facts)
	}
	if !facts.HasGPU || facts.GPUCount != 2 {
		t.Fatalf("gpu: %+v", facts)
	}
	if facts.NICCount != 2 {
		t.Fatalf("nic want 2 got %d", facts.NICCount)
	}
	if facts.PSUCount != 2 {
		t.Fatalf("psu: %d", facts.PSUCount)
	}
	if facts.DriveCount != 4 {
		t.Fatalf("drives: %d summary=%q", facts.DriveCount, facts.StorageSummary)
	}
	if facts.StorageCount("HDD") != 2 || facts.StorageCount("NVMe") != 1 || facts.StorageCount("SATA-SSD") != 1 {
		t.Fatalf("storage buckets: %+v", facts.Storage)
	}
	if facts.StorageSummary != "HDD×2 · SATA-SSD×1 · NVMe×1" {
		t.Fatalf("storage summary: %q", facts.StorageSummary)
	}
}

func TestHardwareFactsPersistAndAttach(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.CreateServer(Server{
		Name:     "node1",
		Endpoint: "https://10.0.0.1",
		Username: "root",
		Password: "x",
		Status:   StatusBound,
		Source:   SourceManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"cpu":[{"id":"1","model":"EPYC 7543","total_cores":32,"total_threads":64}],"ram":[{"id":"a","capacity_mib":65536}],"gpu":[{"id":"g","model":"A100"}],"network":[{"id":"n","mac_address":"aa:bb:cc:dd:ee:ff"}],"psu":[{"id":"p"}],"storage":[{"id":"d","serial_number":"X","media_type":"HDD","protocol":"SAS","capacity_gib":2000}]}`
	if err := st.SaveSnapshot(id, "inventory", payload); err != nil {
		t.Fatal(err)
	}
	if err := st.RefreshHardwareFactsFromInventory(id, payload); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Hardware == nil {
		t.Fatalf("expected attached hardware: %+v", list)
	}
	hw := list[0].Hardware
	if hw.CPUCount != 1 || hw.RAMGiB != 64 || !hw.HasGPU || hw.NICCount != 1 || hw.StorageCount("HDD") != 1 {
		t.Fatalf("hw: %+v", hw)
	}
}

func TestBackfillHardwareFactsFromSnapshots(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateServer(Server{
		Name: "n", Endpoint: "https://1.2.3.4", Username: "u", Password: "p",
		Status: StatusBound, Source: SourceManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSnapshot(id, "inventory", `{"cpu":[{"id":"1","model":"Xeon","total_cores":8,"total_threads":16}]}`); err != nil {
		t.Fatal(err)
	}
	n, err := st.BackfillHardwareFactsFromSnapshots()
	if err != nil || n != 1 {
		t.Fatalf("backfill n=%d err=%v", n, err)
	}
	srv, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Hardware == nil || srv.Hardware.CPUCount != 1 {
		t.Fatalf("get: %+v", srv.Hardware)
	}
}
