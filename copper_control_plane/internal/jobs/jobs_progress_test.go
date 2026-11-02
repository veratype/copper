package jobs

import "testing"

func TestDownloadProgressLogWorthy(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"DOWNLOAD_PROGRESS pct=0 bytes=0 total=100", true},
		{"DOWNLOAD_PROGRESS pct=25 bytes=1 total=100", true},
		{"DOWNLOAD_PROGRESS pct=50 bytes=1 total=100", true},
		{"DOWNLOAD_PROGRESS pct=47 bytes=1 total=100", false},
		{"DOWNLOAD_PROGRESS pct=100 bytes=100 total=100", true},
		{"DOWNLOAD_PROGRESS bytes=1234", true},
	}
	for _, tc := range cases {
		if got := downloadProgressLogWorthy(tc.line); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.line, got, tc.want)
		}
	}
}
