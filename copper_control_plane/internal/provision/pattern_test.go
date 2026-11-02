package provision

import "testing"

func TestExpandPatternHostname(t *testing.T) {
	cases := []struct {
		pat, name string
		index     int
		want      string
	}{
		{"", "srv-a", 1, "srv-a"},
		{"{name}", "srv-a", 1, "srv-a"},
		{"node-{n}", "x", 1, "node-1"},
		{"node-{nn}", "x", 3, "node-03"},
		{"node-{nnn}", "x", 7, "node-007"},
		{"rack-{n+10}", "x", 1, "rack-11"},
		{"{name}-{nn}", "web", 2, "web-02"},
		{"prefix-{nn+5}", "x", 1, "prefix-06"},
	}
	for _, tc := range cases {
		got, err := ExpandPattern(tc.pat, tc.name, tc.index)
		if err != nil {
			t.Fatalf("%q: %v", tc.pat, err)
		}
		if got != tc.want {
			t.Fatalf("%q index=%d: got %q want %q", tc.pat, tc.index, got, tc.want)
		}
	}
}

func TestExpandAddressCIDR(t *testing.T) {
	got, err := ExpandAddressCIDR("10.0.0.{n+10}/24", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.0.11/24" {
		t.Fatalf("got %q", got)
	}
	got, err = ExpandAddressCIDR("10.0.{n}.20/24", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.3.20/24" {
		t.Fatalf("got %q", got)
	}
	_, err = ExpandAddressCIDR("10.0.0.{n+250}/24", 10)
	if err == nil {
		t.Fatal("expected error for octet overflow / invalid CIDR")
	}
}

func TestHasIndexPattern(t *testing.T) {
	if !HasIndexPattern("10.0.0.{n}/24") {
		t.Fatal("expected true")
	}
	if HasIndexPattern("{name}") {
		t.Fatal("name-only should be false")
	}
	if HasIndexPattern("10.0.0.5/24") {
		t.Fatal("literal should be false")
	}
}

func TestExpandPatternRejectsUnknownToken(t *testing.T) {
	_, err := ExpandPattern("{myhost}-{n}", "myhost", 1)
	if err == nil {
		t.Fatal("expected error for {myhost}")
	}
	_, err = ExpandPattern("{name}-{n}", "myhost", 1)
	if err != nil {
		t.Fatal(err)
	}
}
