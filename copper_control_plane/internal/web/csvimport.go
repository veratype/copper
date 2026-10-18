package web

import (
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"strings"

	"copper_control_plane/internal/store"
)

// CSV columns (header required, order flexible):
//
//	chassis_id,bmc_ip,mac_address,username,password
//
// username + password required; at least one of chassis_id, bmc_ip, mac_address.

type csvImportRow struct {
	Line      int
	ChassisID string
	BMCIP     string
	MAC       string
	Username  string
	Password  string
}

type csvRowError struct {
	Line int
	Err  string
}

type csvImportResult struct {
	Created  int
	Updated  int
	Skipped  int
	RowErrs  []csvRowError
	ParseErr string
}

func parseServersCSV(r io.Reader) (rows []csvImportRow, result csvImportResult) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // variable; we validate ourselves

	header, err := cr.Read()
	if err != nil {
		result.ParseErr = "CSV is empty or unreadable"
		return nil, result
	}
	col, err := mapCSVHeader(header)
	if err != nil {
		result.ParseErr = err.Error()
		return nil, result
	}

	line := 1 // header
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			result.RowErrs = append(result.RowErrs, csvRowError{Line: line, Err: err.Error()})
			continue
		}
		if csvRecordEmpty(rec) {
			continue
		}
		row, err := validateCSVRow(line, rec, col)
		if err != nil {
			result.RowErrs = append(result.RowErrs, csvRowError{Line: line, Err: err.Error()})
			continue
		}
		rows = append(rows, row)
	}
	return rows, result
}

func mapCSVHeader(header []string) (map[string]int, error) {
	want := map[string]bool{
		"chassis_id":  true,
		"bmc_ip":      true,
		"mac_address": true,
		"username":    true,
		"password":    true,
	}
	col := map[string]int{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		key = strings.TrimPrefix(key, "\ufeff") // BOM
		if !want[key] {
			continue
		}
		if _, dup := col[key]; dup {
			return nil, fmt.Errorf("duplicate column %q", key)
		}
		col[key] = i
	}
	for _, req := range []string{"username", "password"} {
		if _, ok := col[req]; !ok {
			return nil, fmt.Errorf("missing required column %q (need chassis_id,bmc_ip,mac_address,username,password)", req)
		}
	}
	hasIDCol := false
	for _, k := range []string{"chassis_id", "bmc_ip", "mac_address"} {
		if _, ok := col[k]; ok {
			hasIDCol = true
			break
		}
	}
	if !hasIDCol {
		return nil, fmt.Errorf("header must include at least one of: chassis_id, bmc_ip, mac_address")
	}
	return col, nil
}

func csvRecordEmpty(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func fieldAt(rec []string, col map[string]int, name string) string {
	i, ok := col[name]
	if !ok || i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func validateCSVRow(line int, rec []string, col map[string]int) (csvImportRow, error) {
	row := csvImportRow{
		Line:      line,
		ChassisID: fieldAt(rec, col, "chassis_id"),
		BMCIP:     fieldAt(rec, col, "bmc_ip"),
		MAC:       fieldAt(rec, col, "mac_address"),
		Username:  fieldAt(rec, col, "username"),
		Password:  fieldAt(rec, col, "password"),
	}

	// Preprocess: user/pass + at least one identity ⇒ at least 3 populated fields.
	filled := 0
	if row.Username != "" {
		filled++
	}
	if row.Password != "" {
		filled++
	}
	idCount := 0
	if row.ChassisID != "" {
		idCount++
		filled++
	}
	if row.BMCIP != "" {
		idCount++
		filled++
	}
	if row.MAC != "" {
		idCount++
		filled++
	}
	if filled < 3 || row.Username == "" || row.Password == "" || idCount < 1 {
		return row, fmt.Errorf("need username, password, and at least one of chassis_id / bmc_ip / mac_address (got chassis_id=%q bmc_ip=%q mac=%q user=%q)",
			row.ChassisID, row.BMCIP, row.MAC, row.Username)
	}

	if row.BMCIP != "" {
		ip := net.ParseIP(row.BMCIP)
		if ip == nil || ip.To4() == nil {
			return row, fmt.Errorf("invalid bmc_ip %q (need IPv4)", row.BMCIP)
		}
		row.BMCIP = ip.To4().String()
	}
	if row.MAC != "" {
		hw, err := net.ParseMAC(row.MAC)
		if err != nil {
			return row, fmt.Errorf("invalid mac_address %q", row.MAC)
		}
		row.MAC = hw.String()
	}
	return row, nil
}

func (s *Server) applyCSVRows(rows []csvImportRow) csvImportResult {
	var result csvImportResult
	for _, row := range rows {
		existing, err := s.findImportMatch(row)
		if err != nil {
			result.RowErrs = append(result.RowErrs, csvRowError{Line: row.Line, Err: err.Error()})
			continue
		}
		if existing != nil {
			updated := mergeImportInto(existing, row)
			if err := s.store.UpdateServer(*updated); err != nil {
				result.RowErrs = append(result.RowErrs, csvRowError{Line: row.Line, Err: err.Error()})
				continue
			}
			result.Updated++
			continue
		}
		srv := serverFromCSVRow(row)
		if _, err := s.store.CreateServer(srv); err != nil {
			result.RowErrs = append(result.RowErrs, csvRowError{Line: row.Line, Err: err.Error()})
			continue
		}
		result.Created++
	}
	return result
}

func (s *Server) findImportMatch(row csvImportRow) (*store.Server, error) {
	if row.MAC != "" {
		srv, err := s.store.FindServerByMAC(row.MAC)
		if err != nil || srv != nil {
			return srv, err
		}
	}
	if row.ChassisID != "" {
		srv, err := s.store.FindServerByChassisID(row.ChassisID)
		if err != nil || srv != nil {
			return srv, err
		}
	}
	if row.BMCIP != "" {
		srv, err := s.store.FindServerByBMCIP(row.BMCIP)
		if err != nil || srv != nil {
			return srv, err
		}
	}
	return nil, nil
}

func serverFromCSVRow(row csvImportRow) store.Server {
	name := row.ChassisID
	if name == "" {
		name = row.MAC
	}
	if name == "" {
		name = row.BMCIP
	}
	endpoint := ""
	if row.BMCIP != "" {
		endpoint = store.EndpointFromIP(row.BMCIP)
	}
	return store.Server{
		Name:      name,
		Endpoint:  endpoint,
		Username:  row.Username,
		Password:  row.Password,
		Insecure:  true,
		MAC:       row.MAC,
		ChassisID: row.ChassisID,
		Status:    store.StatusDeclared,
		Source:    store.SourceImport,
		Notes:     "imported from CSV",
	}
}

func mergeImportInto(existing *store.Server, row csvImportRow) *store.Server {
	srv := *existing
	if row.Username != "" {
		srv.Username = row.Username
	}
	if row.Password != "" {
		srv.Password = row.Password
	}
	if row.ChassisID != "" && srv.ChassisID == "" {
		srv.ChassisID = row.ChassisID
	}
	if row.MAC != "" && srv.MAC == "" {
		srv.MAC = row.MAC
	}
	if row.BMCIP != "" && store.EndpointHost(srv.Endpoint) == "" {
		srv.Endpoint = store.EndpointFromIP(row.BMCIP)
	}
	if srv.Source == "" {
		srv.Source = store.SourceImport
	}
	// Re-import refreshes credentials for a declared row; stay declared until DHCP binds.
	if srv.Status == "" {
		srv.Status = store.StatusDeclared
	}
	if srv.Name == "" || srv.Name == store.EndpointHost(srv.Endpoint) || srv.Name == srv.MAC {
		if row.ChassisID != "" {
			srv.Name = row.ChassisID
		}
	}
	return &srv
}
