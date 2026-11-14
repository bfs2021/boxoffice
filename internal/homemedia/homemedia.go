// Package homemedia is Box Office's Home videos and photos section: the
// photos and videos people took themselves, found in the home media root
// folders. Box Office reads when and where each was taken and on what,
// makes a thumbnail, puts photos taken together into albums, and finds
// copies of the same photo or video.
//
// It only reads these files. The one change it makes is moving copies
// someone picked into the root folder's .recycle folder. Thumbnails and the
// town list are kept in the data folder (data/home). Where a photo was
// taken never leaves the server: places are named from a list of towns
// downloaded once.
package homemedia

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yeixio/boxoffice/internal/mediatypes"
	"github.com/yeixio/boxoffice/internal/store"
)

// ErrOff means Home videos and photos isn't turned on in Settings.
var ErrOff = errors.New("turn on Home videos and photos in Settings first")

// ErrBusy means a scan is running already.
var ErrBusy = errors.New("Box Office is looking through your photos and videos already")

// settingScanned is when the last whole scan finished.
const settingScanned = "home_scanned_at"

// Service is the Home videos and photos section.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	// Dir keeps thumbnails, viewable copies and the town list: data/home.
	Dir string
	// Places is another copy of GeoNames' town list (a URL or a file);
	// "off" names no places.
	Places string
	// FFmpeg and FFprobe are the programs used for videos and for photos
	// Go can't read (HEIC, WebP, TIFF); empty finds them on the PATH.
	FFmpeg, FFprobe string
	// Recycle moves a file into a root folder's .recycle folder and says
	// where it went.
	Recycle   func(root, path string) (string, error)
	HTTP      *http.Client
	UserAgent string
	// Location is the time zone videos' times are read in; nil is the
	// server's.
	Location *time.Location

	mu       sync.Mutex
	progress Progress
	again    bool

	towns  places
	viewMu sync.Mutex
	sem    chan struct{}
	once   sync.Once
}

// Progress is the last scan: how far it got, and what it found.
type Progress struct {
	Running bool `json:"running"`
	// Step is what it's doing: finding (looking for files), reading
	// (reading them and making thumbnails), places (naming where they
	// were taken) or albums (putting them into events).
	Step     string    `json:"step,omitempty"`
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Found    int       `json:"found"`
	New      int       `json:"new"`
	Gone     int       `json:"gone"`
	Failed   int       `json:"failed"`
	Skipped  []string  `json:"skipped"` // root folders it couldn't look in
	Note     string    `json:"note,omitempty"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (s *Service) userAgent() string { return cmp.Or(s.UserAgent, "BoxOffice") }

func (s *Service) tool(name, set string) string {
	if set != "" {
		return set
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func (s *Service) ffmpeg() string  { return s.tool("ffmpeg", s.FFmpeg) }
func (s *Service) ffprobe() string { return s.tool("ffprobe", s.FFprobe) }

// slots bounds how many files are read at once, so big photos don't take
// all the memory.
func (s *Service) slots() chan struct{} {
	s.once.Do(func() { s.sem = make(chan struct{}, 2) })
	return s.sem
}

// On says whether Home videos and photos is turned on.
func (s *Service) On(ctx context.Context) error {
	on, err := mediatypes.Enabled(ctx, s.Store, mediatypes.Home)
	if err != nil {
		return err
	}
	if !on {
		return ErrOff
	}
	return nil
}

// Progress gives the last scan's progress.
func (s *Service) Progress() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.progress
	p.Skipped = slices.Clone(p.Skipped)
	if p.Skipped == nil {
		p.Skipped = []string{}
	}
	return p
}

func (s *Service) set(f func(p *Progress)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.progress)
}

// scanTimeout bounds one scan, however big the library.
const scanTimeout = 24 * time.Hour

// Start looks through the home media root folders in the background.
func (s *Service) Start(ctx context.Context) (Progress, error) {
	if err := s.On(ctx); err != nil {
		return Progress{}, err
	}
	s.mu.Lock()
	if s.progress.Running {
		s.mu.Unlock()
		return Progress{}, ErrBusy
	}
	s.progress = Progress{Running: true, Step: "finding", Started: time.Now().UTC(), Skipped: []string{}}
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
		defer cancel()
		err := s.scan(ctx)
		s.set(func(p *Progress) {
			p.Running, p.Step = false, ""
			p.Finished = time.Now().UTC()
			if err != nil {
				p.Error = err.Error()
			}
		})
		if err != nil {
			s.log().Warn("looking through home videos and photos", "err", err)
		}
		s.mu.Lock()
		again := s.again
		s.again = false
		s.mu.Unlock()
		if again {
			if _, err := s.Start(context.Background()); err != nil && !errors.Is(err, ErrBusy) {
				s.log().Warn("looking through home videos and photos", "err", err)
			}
		}
	}()
	return s.Progress(), nil
}

// ScanSoon scans now, or once the scan running finishes, as after a root
// folder is added.
func (s *Service) ScanSoon(ctx context.Context) error {
	_, err := s.Start(ctx)
	if errors.Is(err, ErrBusy) {
		s.mu.Lock()
		s.again = true
		s.mu.Unlock()
		return nil
	}
	return err
}

// Run scans a few minutes after Box Office starts, and twice a day, to
// pick up photos and videos added.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTimer(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(time.Hour)
		if s.On(ctx) != nil {
			continue
		}
		roots, err := s.Store.RootFolders(ctx, mediatypes.Home)
		if err != nil || len(roots) == 0 {
			continue
		}
		last, _ := s.Store.Setting(ctx, settingScanned)
		at, _ := strconv.ParseInt(last, 10, 64)
		if time.Since(time.Unix(at, 0)) < 12*time.Hour {
			continue
		}
		if _, err := s.Start(ctx); err != nil && !errors.Is(err, ErrBusy) {
			s.log().Warn("looking through home videos and photos", "err", err)
		}
	}
}

// Kinds of file, by extension.
var (
	photoExt = map[string]bool{".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true, ".webp": true,
		".heic": true, ".heif": true, ".avif": true, ".tif": true, ".tiff": true, ".bmp": true,
		".dng": true, ".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true, ".orf": true, ".rw2": true, ".pef": true, ".srw": true}
	videoExt = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".avi": true, ".mkv": true, ".mts": true, ".m2ts": true,
		".3gp": true, ".3g2": true, ".mpg": true, ".mpeg": true, ".wmv": true, ".webm": true, ".flv": true, ".dv": true}
	// rawTIFF are raw photos laid out as TIFFs.
	rawTIFF = map[string]bool{".tif": true, ".tiff": true, ".dng": true, ".cr2": true, ".nef": true, ".arw": true, ".orf": true, ".rw2": true, ".pef": true, ".srw": true}
	// goReads are pictures Go decodes itself.
	goReads = map[string]bool{".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true}
	// Browsers show these as they are.
	browserPhoto = map[string]bool{".jpg": true, ".jpeg": true, ".jpe": true, ".png": true, ".gif": true, ".webp": true, ".avif": true, ".bmp": true}
	browserVideo = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true}
)

// KindOf says whether a file is a photo or a video, or neither ("").
func KindOf(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case photoExt[ext]:
		return "photo"
	case videoExt[ext]:
		return "video"
	}
	return ""
}

// skipDir says whether a folder is left out: hidden ones, recycle bins,
// and NAS thumbnail folders.
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "@") || strings.HasPrefix(name, "#") {
		return true
	}
	switch strings.ToLower(name) {
	case "lost+found", "$recycle.bin", "system volume information", "thumbs", ".thumbnails":
		return true
	}
	return false
}

type job struct {
	root, path string
	size       int64
	mtime      int64
	old        *store.HomeItem
}

func (s *Service) scan(ctx context.Context) error {
	roots, err := s.Store.RootFolders(ctx, mediatypes.Home)
	if err != nil {
		return err
	}
	whole := true
	var jobs []job
	type seen struct {
		root  string
		paths map[string]bool
		had   []store.HomeItem
		ok    bool
	}
	var looked []seen
	for _, r := range roots {
		had, err := s.Store.HomeItemsIn(ctx, r.Path)
		if err != nil {
			return err
		}
		byPath := map[string]*store.HomeItem{}
		for i := range had {
			byPath[had[i].Path] = &had[i]
		}
		sn := seen{root: r.Path, paths: map[string]bool{}, had: had, ok: true}
		werr := filepath.WalkDir(r.Path, func(p string, d fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				if p == r.Path {
					return err
				}
				s.log().Debug("can't look in a home media folder", "path", p, "err", err)
				sn.ok = false
				return nil
			}
			if d.IsDir() {
				if p != r.Path && skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !d.Type().IsRegular() || KindOf(p) == "" {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				sn.ok = false
				return nil
			}
			sn.paths[p] = true
			old := byPath[p]
			if old != nil && old.Size == fi.Size() && old.MTime == fi.ModTime().Unix() && old.Thumb != 0 {
				return nil
			}
			jobs = append(jobs, job{root: r.Path, path: p, size: fi.Size(), mtime: fi.ModTime().Unix(), old: old})
			return nil
		})
		if werr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.log().Warn("can't look in a home media root folder", "path", r.Path, "err", werr)
			s.set(func(p *Progress) { p.Skipped = append(p.Skipped, r.Path) })
			whole = false
			continue
		}
		// A share that's dropped out looks empty: forget nothing then.
		if len(sn.paths) == 0 && len(had) > 0 {
			s.set(func(p *Progress) {
				p.Skipped = append(p.Skipped, r.Path)
				p.Note = "A root folder looked empty, so nothing in it was forgotten. Check it's mounted, then scan again."
			})
			whole = false
			continue
		}
		looked = append(looked, sn)
		s.set(func(p *Progress) { p.Found += len(sn.paths) })
	}
	// Forget files gone from disk, from folders looked through whole.
	for _, sn := range looked {
		if !sn.ok {
			whole = false
			continue
		}
		var gone []int64
		for _, it := range sn.had {
			if !sn.paths[it.Path] {
				gone = append(gone, it.ID)
			}
		}
		if err := s.Store.DeleteHomeItems(ctx, gone); err != nil {
			return err
		}
		s.set(func(p *Progress) { p.Gone += len(gone) })
	}
	// Read the new and changed files.
	s.set(func(p *Progress) { p.Step, p.Total, p.Done = "reading", len(jobs), 0 })
	if err := s.readAll(ctx, jobs); err != nil {
		return err
	}
	// Name the places of photos the town list wasn't there for before.
	if err := s.namePlaces(ctx); err != nil {
		s.log().Info("not naming places", "err", err)
		s.set(func(p *Progress) {
			if p.Note == "" {
				p.Note = "Places aren't named yet: " + err.Error()
			}
		})
	}
	s.set(func(p *Progress) { p.Step = "albums" })
	all, err := s.Store.AllHomeItems(ctx)
	if err != nil {
		return err
	}
	if err := s.Store.SyncHomeEvents(ctx, findEvents(all)); err != nil {
		return err
	}
	if whole {
		s.sweep(all)
	}
	return s.Store.SetSetting(ctx, settingScanned, strconv.FormatInt(time.Now().Unix(), 10))
}

func (s *Service) readAll(ctx context.Context, jobs []job) error {
	var wg sync.WaitGroup
	slots := s.slots()
	var firstErr error
	var errMu sync.Mutex
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-slots }()
			it, err := s.read(ctx, j)
			if err != nil {
				s.log().Debug("can't read a home photo or video", "path", j.path, "err", err)
				s.set(func(p *Progress) { p.Failed++; p.Done++ })
				return
			}
			if _, err := s.Store.PutHomeItem(ctx, it); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
			s.set(func(p *Progress) {
				p.Done++
				if j.old == nil {
					p.New++
				}
			})
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// readLimit is the biggest photo read whole, for its exact fingerprint and
// a thumbnail made in Go.
const readLimit = 150 << 20

// read reads a photo or video: when and where it was taken, on what, its
// fingerprint, and its thumbnail.
func (s *Service) read(ctx context.Context, j job) (store.HomeItem, error) {
	it := store.HomeItem{RootFolder: j.root, Path: j.path, Kind: KindOf(j.path), Size: j.size, MTime: j.mtime}
	if j.old != nil {
		it.Place = j.old.Place
	}
	ext := strings.ToLower(filepath.Ext(j.path))
	var taken time.Time
	var from string
	if it.Kind == "photo" {
		var b []byte
		var err error
		if j.size <= readLimit {
			if b, err = os.ReadFile(j.path); err != nil {
				return it, err
			}
			h := sha256.Sum256(b)
			it.Sum = hex.EncodeToString(h[:16])
		} else if it.Sum, err = sampledSum(j.path, j.size); err != nil {
			return it, err
		}
		m := s.photoMeta(b, ext)
		if !m.Taken.IsZero() {
			taken, from = m.Taken, "camera"
		}
		it.Make, it.Model, it.Orientation = m.Make, m.Model, m.Orientation
		it.Width, it.Height = shown(m.Width, m.Height, m.Orientation)
		if m.HasGPS {
			it.Lat, it.Lon, it.HasGPS = m.Lat, m.Lon, true
		}
		w, h, look, err := s.photoThumb(ctx, j.path, ext, it.Sum, b, m)
		if err != nil {
			s.log().Debug("can't make a photo's thumbnail", "path", j.path, "err", err)
			it.Thumb = -1
		} else {
			it.Thumb, it.Look = 1, look
			if it.Width == 0 {
				it.Width, it.Height = w, h
			}
		}
	} else {
		var err error
		if it.Sum, err = sampledSum(j.path, j.size); err != nil {
			return it, err
		}
		if probe := s.ffprobe(); probe != "" {
			if m, err := probeVideo(ctx, probe, j.path, s.Location); err == nil {
				if !m.Taken.IsZero() {
					taken, from = m.Taken, "video"
				}
				it.Duration, it.Width, it.Height, it.Make, it.Model = m.Duration, m.Width, m.Height, m.Make, m.Model
				if m.HasGPS {
					it.Lat, it.Lon, it.HasGPS = m.Lat, m.Lon, true
				}
			}
		}
		it.Thumb = -1
		if ff := s.ffmpeg(); ff != "" {
			at := 0.0
			if it.Duration > 2 {
				at = min(1, it.Duration/3)
			}
			if err := ffmpegFrame(ctx, ff, j.path, s.thumbPath(it.Sum), thumbSize, at); err == nil {
				it.Thumb = 1
			} else {
				s.log().Debug("can't make a video's thumbnail", "path", j.path, "err", err)
			}
		}
	}
	if taken.IsZero() {
		if t, ok := dateFromName(filepath.Base(j.path)); ok {
			taken, from = t, "name"
		} else {
			taken, from = wallClock(time.Unix(j.mtime, 0), s.Location), "file"
		}
	}
	it.Taken, it.TakenFrom = taken.Unix(), from
	if it.HasGPS && it.Place == "" {
		if err := s.loadTowns(ctx); err == nil {
			it.Place = s.towns.name(it.Lat, it.Lon)
		}
	}
	return it, nil
}

// photoMeta reads a photo's EXIF from its bytes.
func (s *Service) photoMeta(b []byte, ext string) Meta {
	if b == nil {
		return Meta{}
	}
	switch {
	case ext == ".jpg" || ext == ".jpeg" || ext == ".jpe":
		if m, err := readJPEG(b); err == nil {
			return m
		}
	case rawTIFF[ext]:
		if m, err := readTIFF(bytes.NewReader(b), 0); err == nil {
			return m
		}
	case ext == ".raf":
		if off, n, ok := rafPreview(b); ok {
			m, _ := readJPEG(b[off : off+n])
			m.Previews = [][2]int64{{off, n}}
			return m
		}
	}
	if m, ok := findEXIF(b[:min(len(b), 4<<20)]); ok {
		return m
	}
	return Meta{}
}

// rafPreview finds the JPEG inside a Fujifilm raw photo.
func rafPreview(b []byte) (int64, int64, bool) {
	if len(b) < 92 || !bytes.HasPrefix(b, []byte("FUJIFILMCCD-RAW")) {
		return 0, 0, false
	}
	off := int64(binary.BigEndian.Uint32(b[84:]))
	n := int64(binary.BigEndian.Uint32(b[88:]))
	if off <= 0 || n <= 0 || off+n > int64(len(b)) {
		return 0, 0, false
	}
	return off, n, true
}

// picture decodes the picture a photo shows, for those Go can read: the
// photo itself, or the biggest JPEG kept inside a raw one.
func picture(b []byte, ext string, m Meta) (image.Image, error) {
	if goReads[ext] {
		return decodeSmall(b)
	}
	var best [2]int64
	for _, p := range m.Previews {
		if p[1] > best[1] && p[0]+p[1] <= int64(len(b)) && p[1] > 2 && b[p[0]] == 0xFF && b[p[0]+1] == 0xD8 {
			best = p
		}
	}
	if best[1] == 0 {
		return nil, errors.New("Box Office can't read this kind of picture itself")
	}
	return decodeSmall(b[best[0] : best[0]+best[1]])
}

// photoThumb makes a photo's thumbnail and says its size and look.
func (s *Service) photoThumb(ctx context.Context, path, ext, sum string, b []byte, m Meta) (int, int, string, error) {
	out := s.thumbPath(sum)
	if b != nil {
		if img, err := picture(b, ext, m); err == nil {
			small := scaled(img, thumbSize, m.Orientation)
			w, h := shown(img.Bounds().Dx(), img.Bounds().Dy(), m.Orientation)
			if err := writeJPEG(out, small, 82); err != nil {
				return 0, 0, "", err
			}
			return w, h, lookOf(small), nil
		}
	}
	ff := s.ffmpeg()
	if ff == "" {
		return 0, 0, "", errors.New("FFmpeg isn't installed")
	}
	if err := ffmpegFrame(ctx, ff, path, out, thumbSize, 0); err != nil {
		return 0, 0, "", err
	}
	tb, err := os.ReadFile(out)
	if err != nil {
		return 0, 0, "", err
	}
	img, err := decodeSmall(tb)
	if err != nil {
		return 0, 0, "", err
	}
	return 0, 0, lookOf(img), nil
}

// sampledSum fingerprints a big file from its size and its first and last
// megabyte, so a video isn't read whole.
func sampledSum(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	fmt.Fprintf(h, "%d:", size)
	const part = 1 << 20
	if _, err := io.CopyN(h, f, min(part, size)); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if size > 2*part {
		if _, err := f.Seek(size-part, io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.CopyN(h, f, part); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	return "s" + hex.EncodeToString(h.Sum(nil)[:16]), nil
}

func (s *Service) thumbPath(sum string) string {
	return filepath.Join(s.Dir, "thumbs", sum[:min(2, len(sum))], sum+".jpg")
}

func (s *Service) previewPath(sum string) string {
	return filepath.Join(s.Dir, "previews", sum[:min(2, len(sum))], sum+".jpg")
}

func (s *Service) loadTowns(ctx context.Context) error {
	if strings.EqualFold(s.Places, "off") {
		return errors.New("naming places is turned off")
	}
	return s.towns.load(ctx, s)
}

// namePlaces names where photos with a place in them were taken, for those
// read before the town list was there.
func (s *Service) namePlaces(ctx context.Context) error {
	all, err := s.Store.AllHomeItems(ctx)
	if err != nil {
		return err
	}
	var todo []store.HomeItem
	for _, it := range all {
		if it.HasGPS && it.Place == "" {
			todo = append(todo, it)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if err := s.loadTowns(ctx); err != nil {
		return err
	}
	s.set(func(p *Progress) { p.Step, p.Done, p.Total = "places", 0, len(todo) })
	for _, it := range todo {
		if name := s.towns.name(it.Lat, it.Lon); name != "" {
			if err := s.Store.SetHomePlace(ctx, it.ID, name); err != nil {
				return err
			}
		}
		s.set(func(p *Progress) { p.Done++ })
	}
	return nil
}

// sweep deletes thumbnails and viewable copies of files no longer there.
func (s *Service) sweep(all []store.HomeItem) {
	keep := map[string]bool{}
	for _, it := range all {
		keep[it.Sum+".jpg"] = true
	}
	for _, dir := range []string{"thumbs", "previews"} {
		root := filepath.Join(s.Dir, dir)
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !keep[d.Name()] {
				os.Remove(p)
			}
			return nil
		})
	}
}

// ErrNoThumb means a photo or video has no thumbnail.
var ErrNoThumb = errors.New("there's no thumbnail for this one")

// Thumb is the file of a photo or video's thumbnail.
func (s *Service) Thumb(it store.HomeItem) (string, error) {
	if it.Thumb != 1 {
		return "", ErrNoThumb
	}
	p := s.thumbPath(it.Sum)
	if _, err := os.Stat(p); err != nil {
		return "", ErrNoThumb
	}
	return p, nil
}

// Viewable says whether a browser can show a photo or play a video as it
// is.
func Viewable(it store.HomeItem) bool {
	ext := strings.ToLower(filepath.Ext(it.Path))
	if it.Kind == "video" {
		return browserVideo[ext]
	}
	return browserPhoto[ext]
}

// View is a file a browser can show for a photo: the photo itself, or for
// HEIC, TIFF and raw photos a JPEG made from it (kept for next time).
func (s *Service) View(ctx context.Context, it store.HomeItem) (string, error) {
	if Viewable(it) || it.Kind == "video" {
		return it.Path, nil
	}
	out := s.previewPath(it.Sum)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	ext := strings.ToLower(filepath.Ext(it.Path))
	if it.Size <= readLimit {
		if b, err := os.ReadFile(it.Path); err == nil {
			m := s.photoMeta(b, ext)
			if img, err := picture(b, ext, m); err == nil {
				if err := writeJPEG(out, scaled(img, previewSize, m.Orientation), 88); err != nil {
					return "", err
				}
				return out, nil
			}
		}
	}
	ff := s.ffmpeg()
	if ff == "" {
		return "", errors.New("FFmpeg isn't installed, so Box Office can't show this kind of photo")
	}
	if err := ffmpegFrame(ctx, ff, it.Path, out, previewSize, 0); err != nil {
		return "", err
	}
	return out, nil
}

// Dupes lists the copies found.
func (s *Service) Dupes(ctx context.Context) ([]Dupes, error) {
	all, err := s.Store.AllHomeItems(ctx)
	if err != nil {
		return nil, err
	}
	out := findDupes(all)
	if out == nil {
		out = []Dupes{}
	}
	return out, nil
}

// RecycleError says why copies weren't moved.
type RecycleError string

func (e RecycleError) Error() string { return string(e) }

// RecycleCopies moves copies into their root folder's .recycle folder and
// forgets them. Each must be a copy of something kept: at least one of
// its group stays. size is the space the copies take, as was shown;
// if the copies have changed since, nothing is moved.
func (s *Service) RecycleCopies(ctx context.Context, ids []int64, size int64) (int, int64, error) {
	if s.Recycle == nil {
		return 0, 0, RecycleError("moving copies isn't set up on this server")
	}
	groups, err := s.Dupes(ctx)
	if err != nil {
		return 0, 0, err
	}
	pick := map[int64]bool{}
	for _, id := range ids {
		pick[id] = true
	}
	var move []store.HomeItem
	var total int64
	for _, g := range groups {
		kept := 0
		var here []store.HomeItem
		for _, it := range g.Items {
			if pick[it.ID] {
				here = append(here, it)
			} else {
				kept++
			}
		}
		if len(here) > 0 && kept == 0 {
			return 0, 0, RecycleError("keep at least one of each photo or video")
		}
		for _, it := range here {
			move = append(move, it)
			total += it.Size
			delete(pick, it.ID)
		}
	}
	if len(pick) > 0 {
		return 0, 0, RecycleError("some of these aren't copies of anything any more: look at the list again")
	}
	if total != size {
		return 0, 0, RecycleError("the copies have changed since they were shown: look at the list again")
	}
	var moved []int64
	var freed int64
	var failed error
	for _, it := range move {
		if _, err := s.Recycle(it.RootFolder, it.Path); err != nil {
			failed = fmt.Errorf("couldn't move %s: %w", filepath.Base(it.Path), err)
			break
		}
		moved = append(moved, it.ID)
		freed += it.Size
	}
	if err := s.Store.DeleteHomeItems(ctx, moved); err != nil {
		return len(moved), freed, err
	}
	return len(moved), freed, failed
}
