package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLabelList(t *testing.T) {
	got, err := ParseLabelList("prod, GPU; rack-1\nrack-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "prod" || got[1] != "GPU" || got[2] != "rack-1" {
		t.Fatalf("got %#v", got)
	}
	empty, err := ParseLabelList("  , ; \n ")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %#v err=%v", empty, err)
	}
}

func TestAddInstalledLabelToManyServers(t *testing.T) {
	// Regression: ensureLabelID must not reuse a stale LastInsertId when the
	// "installed" row already exists — otherwise only the first host is tagged.
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var ids []int64
	for i := 1; i <= 3; i++ {
		id, err := st.CreateServer(Server{
			Name: fmt.Sprintf("n%d", i), Username: "u", Password: "p",
			Endpoint: fmt.Sprintf("https://10.0.0.%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Simulate other autoincrement activity between label attaches.
	_, _ = st.CreateServer(Server{
		Name: "noise", Username: "u", Password: "p", MAC: "aa:bb:cc:dd:ee:ff",
	})

	for _, id := range ids {
		if err := st.AddServerLabels(id, []string{LabelInstalled}); err != nil {
			t.Fatalf("server %d: %v", id, err)
		}
	}
	for _, id := range ids {
		srv, err := st.GetServer(id)
		if err != nil {
			t.Fatal(err)
		}
		if !ServerHasAllLabels(*srv, []string{LabelInstalled}) {
			t.Fatalf("server %d labels=%v want installed", id, srv.Labels)
		}
	}
}

func TestServerLabelsCRUD(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
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
	if err := st.SetServerLabels(id, []string{"prod", "gpu", "prod"}); err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Labels) != 2 {
		t.Fatalf("labels=%v", srv.Labels)
	}
	if err := st.AddServerLabels(id, []string{"rack-a", "GPU"}); err != nil {
		t.Fatal(err)
	}
	srv, _ = st.GetServer(id)
	if len(srv.Labels) != 3 {
		t.Fatalf("after add labels=%v", srv.Labels)
	}
	if err := st.RemoveServerLabels(id, []string{"gpu"}); err != nil {
		t.Fatal(err)
	}
	srv, _ = st.GetServer(id)
	for _, l := range srv.Labels {
		if strings.EqualFold(l, "gpu") {
			t.Fatalf("gpu still present: %v", srv.Labels)
		}
	}
	id2, err := st.CreateServer(Server{
		Name: "n2", Username: "u", Password: "p", MAC: "aa:bb:cc:dd:ee:02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddLabelsToServers([]int64{id, id2}, []string{"batch"}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list len %d", len(list))
	}
	for _, s := range list {
		if !ServerHasAllLabels(s, []string{"batch"}) {
			t.Fatalf("server %s missing batch: %v", s.Name, s.Labels)
		}
	}
	all, err := st.ListLabels()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 {
		t.Fatalf("catalog=%v", all)
	}
}
