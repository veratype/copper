package store

import (
	"path/filepath"
	"testing"

	"copper_dhcp"
)

func TestPurgeInventoryAndDHCP(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.CreateServer(Server{
		Name: "n1", Username: "u", Password: "p", Endpoint: "https://10.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetServerLabels(id, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveSnapshot(id, "inventory", `{}`)
	_ = st.SaveAgentDHCPConfig(copper_dhcp.Config{Enabled: true, SubnetCIDR: "10.0.0.0/24"})
	_ = st.SaveLocalDHCPConfig(copper_dhcp.Config{Enabled: true, SubnetCIDR: "10.1.0.0/24"})
	_ = st.ReplaceDataDHCPLeases([]copper_dhcp.Lease{})
	_ = st.SaveBatchRuntime(DefaultBatchRuntime())

	if err := st.PurgeInventoryAndDHCP(); err != nil {
		t.Fatal(err)
	}
	servers, err := st.ListServers()
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers left: %v err=%v", servers, err)
	}
	labels, err := st.ListLabels()
	if err != nil || len(labels) != 0 {
		t.Fatalf("labels left: %v", labels)
	}
	cfg, err := st.GetAgentDHCPConfig()
	if err != nil || cfg.Enabled {
		t.Fatalf("agent dhcp still present: %+v err=%v", cfg, err)
	}
	rt, err := st.GetBatchRuntime()
	if err != nil || rt.Executors != DefaultBatchExecutors {
		t.Fatalf("batch runtime should be kept: %+v err=%v", rt, err)
	}
}
