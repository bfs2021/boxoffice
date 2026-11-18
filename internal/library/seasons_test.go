package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeasonFolderIn(t *testing.T) {
	for _, c := range []struct {
		dirs   []string
		season int
		def    string
		want   string
	}{
		{nil, 3, "Season 03", "Season 03"},
		{[]string{"Season 1", "Season 3"}, 3, "Season 03", "Season 3"},
		{[]string{"Season 3", "Season 03"}, 3, "Season 03", "Season 03"},
		{[]string{"season 003"}, 3, "Season 03", "season 003"},
		{[]string{"S03"}, 3, "Season 03", "S03"},
		{[]string{"Series 3"}, 3, "Season 03", "Series 3"},
		{[]string{"Season 13", "Season 30"}, 3, "Season 03", "Season 03"},
		{[]string{"Season 3 Extras"}, 3, "Season 03", "Season 03"},
		{[]string{"Specials"}, 0, "Season 00", "Specials"},
		{[]string{"Season 0"}, 0, "Specials", "Season 0"},
		{[]string{"Season 1"}, 10, "Season 10", "Season 10"},
	} {
		if got := SeasonFolderIn(c.dirs, c.season, c.def); got != c.want {
			t.Errorf("SeasonFolderIn(%q, %d, %q) = %q, want %q", c.dirs, c.season, c.def, got, c.want)
		}
	}
}

func TestSeasonFolderReadsShow(t *testing.T) {
	show := t.TempDir()
	if err := os.Mkdir(filepath.Join(show, "Season 3"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file named like a season isn't a season folder.
	if err := os.WriteFile(filepath.Join(show, "Season 4"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SeasonFolder(show, 3, "Season 03"); got != "Season 3" {
		t.Errorf("season 3: got %q", got)
	}
	if got := SeasonFolder(show, 4, "Season 04"); got != "Season 04" {
		t.Errorf("season 4: got %q", got)
	}
	if got := SeasonFolder(filepath.Join(show, "missing"), 3, "Season 03"); got != "Season 03" {
		t.Errorf("missing show: got %q", got)
	}
}
