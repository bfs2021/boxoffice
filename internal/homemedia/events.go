package homemedia

import (
	"cmp"
	"slices"

	"github.com/yeixio/boxoffice/internal/store"
)

// How photos are put together into events.
const (
	// eventGap is the longest time between two photos of the same day out.
	eventGap = 8 * 3600
	// tripGap is the longest time between two photos of the same trip:
	// both taken away from home, near each other.
	tripGap   = 48 * 3600
	tripRange = 400.0 // km
	// eventMin is the fewest photos and videos an event needs.
	eventMin = 8
)

// findEvents puts photos and videos taken together into events: a day
// out, a party, a trip. A new event starts after a long enough break,
// unless both sides of it were taken away from home and near each other,
// as on a trip. Home is the place most photos were taken.
func findEvents(items []store.HomeItem) []store.HomeEvent {
	list := make([]store.HomeItem, 0, len(items))
	for _, it := range items {
		if it.Taken > 0 {
			list = append(list, it)
		}
	}
	slices.SortFunc(list, func(a, b store.HomeItem) int { return cmp.Or(cmp.Compare(a.Taken, b.Taken), cmp.Compare(a.ID, b.ID)) })
	home := mostCommon(list)
	away := func(it store.HomeItem) bool { return it.Place != "" && it.Place != home && it.HasGPS }
	var out []store.HomeEvent
	var run []store.HomeItem
	// lastAway is the last photo of the run with a place away from home.
	var lastAway *store.HomeItem
	flush := func() {
		if len(run) >= eventMin {
			ev := store.HomeEvent{Place: mostCommon(run), Start: run[0].Taken, End: run[len(run)-1].Taken}
			for _, it := range run {
				ev.Items = append(ev.Items, it.ID)
			}
			out = append(out, ev)
		}
		run, lastAway = nil, nil
	}
	for i := range list {
		it := list[i]
		if len(run) > 0 {
			gap := it.Taken - run[len(run)-1].Taken
			trip := lastAway != nil && away(it) && it.Taken-lastAway.Taken <= tripGap &&
				km(it.Lat, it.Lon, lastAway.Lat, lastAway.Lon) <= tripRange
			if gap > eventGap && !trip {
				flush()
			}
		}
		run = append(run, it)
		if away(it) {
			lastAway = &list[i]
		}
	}
	flush()
	return out
}

// mostCommon is the place most of these were taken, or "".
func mostCommon(items []store.HomeItem) string {
	n := map[string]int{}
	best, most := "", 0
	for _, it := range items {
		if it.Place == "" {
			continue
		}
		n[it.Place]++
		if c := n[it.Place]; c > most || (c == most && it.Place < best) {
			best, most = it.Place, c
		}
	}
	return best
}
