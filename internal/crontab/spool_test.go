package crontab

import "testing"

// A Debian or Ubuntu machine keeps root's crontab in
// /var/spool/cron/crontabs/root, and cronie one level up. The detail screen,
// the confirm dialog and a converted timer's Description all name this path,
// so naming the other family's file sends a reader to a file that is not there.
func TestSpoolDirFollowsTheInstalledCron(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing map[string]bool
		want     string
		root     string
	}{
		{
			name:     "debian and ubuntu",
			existing: map[string]bool{SpoolDirCronie: true, SpoolDirDebian: true},
			want:     SpoolDirDebian,
			root:     "/var/spool/cron/crontabs/root",
		},
		{
			name:     "fedora, rhel and arch",
			existing: map[string]bool{SpoolDirCronie: true},
			want:     SpoolDirCronie,
			root:     "/var/spool/cron/root",
		},
		{
			// cronie's spool is mode 700, so an unprivileged stat of anything
			// under it fails: that must still read as the cronie layout.
			name:     "cronie spool not searchable",
			existing: map[string]bool{},
			want:     SpoolDirCronie,
			root:     "/var/spool/cron/root",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spoolDirFrom(func(path string) bool { return tc.existing[path] })
			if got != tc.want {
				t.Fatalf("spool dir = %q, want %q", got, tc.want)
			}
			if path := TablePathIn(got, "root"); path != tc.root {
				t.Fatalf("root's table = %q, want %q", path, tc.root)
			}
		})
	}
}

// TablePathFor is the detected directory plus the account, whatever the host.
func TestTablePathForUsesTheDetectedSpool(t *testing.T) {
	if got, want := TablePathFor("ana"), SpoolDir()+"/ana"; got != want {
		t.Fatalf("TablePathFor = %q, want %q", got, want)
	}
}
