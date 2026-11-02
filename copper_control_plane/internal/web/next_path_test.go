package web

import "testing"

func TestSafeNextPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/"},
		{"/storage", "/storage"},
		{"/servers/3/deploy?ids=1", "/servers/3/deploy?ids=1"},
		{"//evil", "/"},
		{"https://evil", "/"},
		{"/login", "/"},
		{"/logout", "/"},
		{"/api/jobs", "/"},
		{"/static/app.js", "/"},
	}
	for _, c := range cases {
		if got := safeNextPath(c.in); got != c.want {
			t.Fatalf("safeNextPath(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
