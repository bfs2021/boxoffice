package homemedia

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// DefaultPlaces is GeoNames' list of towns of 15,000 people or more (CC BY
// 4.0), which places are named from. Only the list is downloaded: where a
// photo was taken never leaves the server.
const DefaultPlaces = "https://download.geonames.org/export/dump/cities15000.zip"

// placeRange is how far a photo can be from a town for it to be named
// after it.
const placeRange = 60.0 // km

type town struct {
	name, country string
	lat, lon      float64
}

// places names where photos were taken, from a town list kept in the data
// folder.
type places struct {
	mu    sync.Mutex
	grid  map[[2]int][]town
	tried time.Time
}

// load reads the town list, downloading it first if it isn't there. A
// failed download is tried again a day later.
func (p *places) load(ctx context.Context, s *Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.grid != nil {
		return nil
	}
	if time.Since(p.tried) < 24*time.Hour {
		return errors.New("the town list couldn't be downloaded; Box Office tries again tomorrow")
	}
	src := s.Places
	if src == "" {
		src = DefaultPlaces
	}
	file := filepath.Join(s.Dir, "towns.txt")
	if _, err := os.Stat(file); err != nil {
		p.tried = time.Now()
		if err := s.fetchTowns(ctx, src, file); err != nil {
			return err
		}
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	grid, err := readTowns(f)
	if err != nil {
		return err
	}
	p.grid = grid
	return nil
}

// fetchTowns downloads GeoNames' zip (or a plain list) and keeps its list.
func (s *Service) fetchTowns(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	var body io.Reader
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", s.userAgent())
		resp, err := s.client().Do(req)
		if err != nil {
			return fmt.Errorf("downloading the town list: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("downloading the town list: %s", resp.Status)
		}
		body = resp.Body
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		body = f
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".towns-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(body, 200<<20)); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading the town list: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// A zip holds the list as its one .txt file.
	if zr, err := zip.OpenReader(tmp.Name()); err == nil {
		defer zr.Close()
		for _, zf := range zr.File {
			if !strings.HasSuffix(zf.Name, ".txt") {
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			out, err := os.Create(dst + ".tmp")
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(rc, 500<<20)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			return os.Rename(dst+".tmp", dst)
		}
		return errors.New("the town list's zip has no list in it")
	}
	return os.Rename(tmp.Name(), dst)
}

// readTowns reads GeoNames' tab-separated list into a grid of 1° squares.
func readTowns(r io.Reader) (map[[2]int][]town, error) {
	grid := map[[2]int][]town{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	names := map[string]string{}
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 9 {
			continue
		}
		lat, err1 := strconv.ParseFloat(f[4], 64)
		lon, err2 := strconv.ParseFloat(f[5], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		cc := f[8]
		country, ok := names[cc]
		if !ok {
			if reg, err := language.ParseRegion(cc); err == nil {
				country = display.English.Regions().Name(reg)
			}
			names[cc] = country
		}
		t := town{name: f[1], country: country, lat: lat, lon: lon}
		k := cell(lat, lon)
		grid[k] = append(grid[k], t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(grid) == 0 {
		return nil, errors.New("the town list is empty")
	}
	return grid, nil
}

func cell(lat, lon float64) [2]int {
	return [2]int{int(math.Floor(lat)), int(math.Floor(lon))}
}

// name gives the town nearest a point, such as "Lisbon, Portugal", or ""
// when none is near.
func (p *places) name(lat, lon float64) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.grid == nil {
		return ""
	}
	c := cell(lat, lon)
	best, bestKm := town{}, math.Inf(1)
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			for _, t := range p.grid[[2]int{c[0] + dy, c[1] + dx}] {
				if d := km(lat, lon, t.lat, t.lon); d < bestKm {
					best, bestKm = t, d
				}
			}
		}
	}
	if bestKm > placeRange {
		return ""
	}
	if best.country == "" {
		return best.name
	}
	return best.name + ", " + best.country
}

// km is the distance between two points on the Earth.
func km(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
