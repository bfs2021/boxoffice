package homemedia

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VideoMeta is what a video says about itself.
type VideoMeta struct {
	Taken       time.Time // clock time, read as UTC
	Duration    float64
	Width       int
	Height      int
	Make, Model string
	Lat, Lon    float64
	HasGPS      bool
}

type probeOut struct {
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType    string            `json:"codec_type"`
		Width        int               `json:"width"`
		Height       int               `json:"height"`
		Tags         map[string]string `json:"tags"`
		SideDataList []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
		Disposition map[string]int `json:"disposition"`
	} `json:"streams"`
}

// probeVideo reads a video's facts with ffprobe.
func probeVideo(ctx context.Context, ffprobe, path string, loc *time.Location) (VideoMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path).Output()
	if err != nil {
		return VideoMeta{}, err
	}
	return parseProbe(out, loc)
}

func parseProbe(out []byte, loc *time.Location) (VideoMeta, error) {
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return VideoMeta{}, err
	}
	var m VideoMeta
	m.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	tags := map[string]string{}
	for k, v := range p.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	for _, s := range p.Streams {
		if s.CodecType != "video" || s.Disposition["attached_pic"] == 1 {
			continue
		}
		m.Width, m.Height = s.Width, s.Height
		rot := 0.0
		if r, err := strconv.ParseFloat(s.Tags["rotate"], 64); err == nil {
			rot = r
		}
		for _, sd := range s.SideDataList {
			if sd.Rotation != 0 {
				rot = sd.Rotation
			}
		}
		if int(math.Abs(rot))%180 == 90 {
			m.Width, m.Height = m.Height, m.Width
		}
		for k, v := range s.Tags {
			if _, ok := tags[strings.ToLower(k)]; !ok {
				tags[strings.ToLower(k)] = v
			}
		}
		break
	}
	// Phones write the local time with its offset; the plain creation
	// time is UTC, a moment, so it's read as Box Office's own clock.
	if t, ok := localTime(tags["com.apple.quicktime.creationdate"]); ok {
		m.Taken = t
	} else if t, err := time.Parse(time.RFC3339Nano, tags["creation_time"]); err == nil && t.Year() > 1970 {
		m.Taken = wallClock(t, loc)
	} else if t, ok := localTime(tags["date"]); ok {
		m.Taken = t
	}
	m.Make = strings.TrimSpace(firstOf(tags, "com.apple.quicktime.make", "make", "com.android.manufacturer"))
	m.Model = strings.TrimSpace(firstOf(tags, "com.apple.quicktime.model", "model", "com.android.model"))
	if la, lo, ok := iso6709(firstOf(tags, "com.apple.quicktime.location.iso6709", "location", "location-eng")); ok {
		m.Lat, m.Lon, m.HasGPS = la, lo, true
	}
	return m, nil
}

func firstOf(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := tags[k]; v != "" {
			return v
		}
	}
	return ""
}

// localTime reads a time with its offset, such as 2024-07-14T15:30:12+0100,
// as the clock time it was there.
func localTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil && t.Year() > 1970 {
			return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC), true
		}
	}
	return time.Time{}, false
}

var iso6709re = regexp.MustCompile(`^([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)`)

// iso6709 reads a place like +38.7223-009.1393+045.000/.
func iso6709(s string) (float64, float64, bool) {
	m := iso6709re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	la, err1 := strconv.ParseFloat(m[1], 64)
	lo, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || math.Abs(la) > 90 || math.Abs(lo) > 180 || (la == 0 && lo == 0) {
		return 0, 0, false
	}
	return la, lo, true
}
