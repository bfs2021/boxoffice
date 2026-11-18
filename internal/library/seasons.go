package library

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var seasonName = regexp.MustCompile(`(?i)^(?:season|series|s)[ ._-]*0*(\d+)$`)

// SeasonFolder is the folder inside a show's folder that holds a season's
// episodes. A folder already there for that season wins, however it's
// written ("Season 3", "season 03", "S03", "Specials" for season 0), so
// an import adds to it instead of making a second one beside it ("Season
// 03" next to "Season 3"). With none there, it's def.
func SeasonFolder(show string, season int, def string) string {
	entries, err := os.ReadDir(show)
	if err != nil {
		return def
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return SeasonFolderIn(dirs, season, def)
}

// SeasonFolderIn is SeasonFolder over a show folder's subfolder names
// already read, for callers that look at many episodes of one show.
func SeasonFolderIn(dirs []string, season int, def string) string {
	if slices.Contains(dirs, def) {
		return def
	}
	var match []string
	for _, d := range dirs {
		if season == 0 && strings.EqualFold(d, "specials") {
			match = append(match, d)
			continue
		}
		m := seasonName.FindStringSubmatch(d)
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n == season {
			match = append(match, d)
		}
	}
	if len(match) == 0 {
		return def
	}
	slices.Sort(match)
	return match[0]
}
