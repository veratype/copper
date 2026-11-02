package web

import "testing"

func TestDeclaredIdentityPresent(t *testing.T) {
	cases := []struct {
		chassis, endpoint, mac string
		ok                     bool
	}{
		{"", "", "", false},
		{"  ", "", "", false},
		{"C1", "", "", true},
		{"", "https://192.168.0.100", "", true},
		{"", "", "7c:c2:55:60:4c:ac", true},
		{"", "", "  aa:bb  ", true},
	}
	for _, tc := range cases {
		got := declaredIdentityPresent(tc.chassis, tc.endpoint, tc.mac)
		if got != tc.ok {
			t.Errorf("declaredIdentityPresent(%q,%q,%q)=%v want %v", tc.chassis, tc.endpoint, tc.mac, got, tc.ok)
		}
	}
}
