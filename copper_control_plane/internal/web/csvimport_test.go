package web

import (
	"strings"
	"testing"
)

func TestParseServersCSV_OK(t *testing.T) {
	in := `chassis_id,bmc_ip,mac_address,username,password
ch-1,,,admin,secret
,10.0.0.5,,root,pw
,,aa:bb:cc:dd:ee:01,ilo,pass
`
	rows, res := parseServersCSV(strings.NewReader(in))
	if res.ParseErr != "" {
		t.Fatalf("parse: %s", res.ParseErr)
	}
	if len(res.RowErrs) != 0 {
		t.Fatalf("row errs: %+v", res.RowErrs)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].ChassisID != "ch-1" || rows[0].Username != "admin" {
		t.Fatalf("row0: %+v", rows[0])
	}
	if rows[1].BMCIP != "10.0.0.5" {
		t.Fatalf("row1 ip: %+v", rows[1])
	}
	if rows[2].MAC == "" {
		t.Fatalf("row2 mac empty: %+v", rows[2])
	}
}

func TestParseServersCSV_RejectMissingIdentity(t *testing.T) {
	in := `chassis_id,bmc_ip,mac_address,username,password
,,,admin,secret
`
	rows, res := parseServersCSV(strings.NewReader(in))
	if res.ParseErr != "" {
		t.Fatalf("parse: %s", res.ParseErr)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 valid rows")
	}
	if len(res.RowErrs) != 1 {
		t.Fatalf("expected 1 row error, got %+v", res.RowErrs)
	}
}

func TestParseServersCSV_RejectMissingPassword(t *testing.T) {
	in := `chassis_id,bmc_ip,mac_address,username,password
ch-1,,,admin,
`
	_, res := parseServersCSV(strings.NewReader(in))
	if len(res.RowErrs) != 1 {
		t.Fatalf("expected row error, got %+v", res.RowErrs)
	}
}
