package buildinfo

import "testing"

func TestProvimuxDisplayVersion(t *testing.T) {
	prevVersion, prevProvimux := Version, ProvimuxVersion
	t.Cleanup(func() { Version, ProvimuxVersion = prevVersion, prevProvimux })

	Version = "8.0.77"
	for _, tc := range []struct{ provimux, want string }{
		{"8.0.77-pmx.1", "8.0.77-pmx.1"},
		{"dev", "8.0.77"},
		{"", "8.0.77"},
	} {
		ProvimuxVersion = tc.provimux
		if got := DisplayVersion(); got != tc.want {
			t.Errorf("ProvimuxVersion=%q: DisplayVersion() = %q, want %q", tc.provimux, got, tc.want)
		}
	}
	if Version != "8.0.77" {
		t.Fatalf("Version changed to %q", Version)
	}
	if ProvimuxRepo != "ngvoicu/provimux" {
		t.Fatalf("ProvimuxRepo = %q", ProvimuxRepo)
	}
}
