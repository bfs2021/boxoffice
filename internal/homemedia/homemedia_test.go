package homemedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yeixio/boxoffice/internal/mediatypes"
	"github.com/yeixio/boxoffice/internal/store"
	"github.com/yeixio/boxoffice/internal/store/storetest"
)

// exifBlock builds an APP1 EXIF segment's TIFF: make, model, orientation,
// DateTimeOriginal and, when lat/lon aren't 0, GPS.
func exifBlock(t *testing.T, mk, model string, orientation int, taken string, lat, lon float64) []byte {
	t.Helper()
	le := binary.LittleEndian
	type ent struct {
		tag, typ uint16
		count    uint32
		data     []byte
	}
	ascii := func(s string) []byte { return append([]byte(s), 0) }
	rational := func(vals ...float64) []byte {
		var b []byte
		for _, v := range vals {
			b = le.AppendUint32(b, uint32(v*10000))
			b = le.AppendUint32(b, 10000)
		}
		return b
	}
	dms := func(v float64) []byte {
		if v < 0 {
			v = -v
		}
		d := float64(int(v))
		m := float64(int((v - d) * 60))
		s := (v - d - m/60) * 3600
		return rational(d, m, s)
	}
	// Lay out IFDs one after another, values after each.
	var out bytes.Buffer
	out.Write([]byte("II*\x00"))
	out.Write(le.AppendUint32(nil, 8))
	write := func(start uint32, entries []ent, next uint32) []byte {
		size := uint32(2 + len(entries)*12 + 4)
		valOff := start + size
		var dir, vals []byte
		dir = le.AppendUint16(dir, uint16(len(entries)))
		for _, e := range entries {
			dir = le.AppendUint16(dir, e.tag)
			dir = le.AppendUint16(dir, e.typ)
			dir = le.AppendUint32(dir, e.count)
			if len(e.data) <= 4 {
				v := make([]byte, 4)
				copy(v, e.data)
				dir = append(dir, v...)
			} else {
				dir = le.AppendUint32(dir, valOff+uint32(len(vals)))
				vals = append(vals, e.data...)
				if len(vals)%2 == 1 {
					vals = append(vals, 0)
				}
			}
		}
		dir = le.AppendUint32(dir, next)
		return append(dir, vals...)
	}
	short := func(v int) []byte { return le.AppendUint16(nil, uint16(v)) }
	long := func(v uint32) []byte { return le.AppendUint32(nil, v) }
	// IFD0 at 8 points at the EXIF IFD and the GPS IFD, written after it.
	ifd0 := func(exifAt, gpsAt uint32) []ent {
		e := []ent{
			{tagMake, 2, uint32(len(mk) + 1), ascii(mk)},
			{tagModel, 2, uint32(len(model) + 1), ascii(model)},
			{tagOrientation, 3, 1, short(orientation)},
			{tagExifIFD, 4, 1, long(exifAt)},
		}
		if lat != 0 || lon != 0 {
			e = append(e, ent{tagGPSIFD, 4, 1, long(gpsAt)})
		}
		return e
	}
	first := write(8, ifd0(0, 0), 0)
	exifAt := 8 + uint32(len(first))
	exif := write(exifAt, []ent{{tagDateOriginal, 2, 20, ascii(taken)}}, 0)
	gpsAt := exifAt + uint32(len(exif))
	latRef, lonRef := "N", "E"
	if lat < 0 {
		latRef = "S"
	}
	if lon < 0 {
		lonRef = "W"
	}
	gps := write(gpsAt, []ent{
		{tagGPSLatRef, 2, 2, ascii(latRef)},
		{tagGPSLat, 5, 3, dms(lat)},
		{tagGPSLonRef, 2, 2, ascii(lonRef)},
		{tagGPSLon, 5, 3, dms(lon)},
	}, 0)
	out.Write(write(8, ifd0(exifAt, gpsAt), 0))
	out.Write(exif)
	out.Write(gps)
	return out.Bytes()
}

// photo makes a JPEG w×h, a gradient with a block in it, with EXIF.
func photo(t *testing.T, w, h int, shade uint8, tiffBlock []byte) []byte {
	t.Helper()
	// A grid of squares in colours picked by shade, so each shade looks
	// different.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(uint64(shade), 7))
	var cells [12][12]uint8
	for y := range cells {
		for x := range cells[y] {
			cells[y][x] = uint8(rng.IntN(256))
		}
	}
	for y := range h {
		for x := range w {
			v := cells[y*12/h][x*12/w]
			img.Set(x, y, color.RGBA{v, v / 2, 255 - v, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if tiffBlock == nil {
		return b
	}
	seg := append([]byte("Exif\x00\x00"), tiffBlock...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(seg)+2))
	app1 = append(app1, seg...)
	return append(append(append([]byte{}, b[:2]...), app1...), b[2:]...)
}

func TestReadJPEG(t *testing.T) {
	b := photo(t, 64, 48, 0, exifBlock(t, "Canon", "EOS R6", 6, "2024:07:14 15:30:12", 38.7223, -9.1393))
	m, err := readJPEG(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Make != "Canon" || m.Model != "EOS R6" || m.Orientation != 6 {
		t.Errorf("camera = %q %q %d", m.Make, m.Model, m.Orientation)
	}
	if want := time.Date(2024, 7, 14, 15, 30, 12, 0, time.UTC); !m.Taken.Equal(want) {
		t.Errorf("taken = %v, want %v", m.Taken, want)
	}
	if !m.HasGPS || m.Lat < 38.72 || m.Lat > 38.73 || m.Lon > -9.13 || m.Lon < -9.14 {
		t.Errorf("gps = %v %v %v", m.HasGPS, m.Lat, m.Lon)
	}
	if m.Width != 64 || m.Height != 48 {
		t.Errorf("size = %dx%d", m.Width, m.Height)
	}
	// A HEIC-like file with the EXIF somewhere inside.
	heic := append([]byte("\x00\x00\x00\x18ftypheic....mdat"), append([]byte("\x00\x00\x00\x06Exif\x00\x00"), exifBlock(t, "Apple", "iPhone 15", 1, "2023:12:25 09:00:00", 0, 0)...)...)
	m, ok := findEXIF(heic)
	if !ok || m.Make != "Apple" || m.Taken.Year() != 2023 {
		t.Errorf("heic = %v %+v", ok, m)
	}
}

func TestDateFromName(t *testing.T) {
	for name, want := range map[string]string{
		"IMG_20240714_153012.jpg":        "2024-07-14 15:30:12",
		"PXL_20231225_090000123.mp4":     "2023-12-25 09:00:00",
		"2019-05-01 18.45.10.jpg":        "2019-05-01 18:45:10",
		"VID-20220302-WA0003.mp4":        "2022-03-02 00:00:00",
		"Screenshot_2021-11-30-10-11-12": "2021-11-30 10:11:12",
		"DSC01234.JPG":                   "",
		"holiday 20240231.jpg":           "",
	} {
		got, ok := dateFromName(name)
		s := ""
		if ok {
			s = got.Format("2006-01-02 15:04:05")
		}
		if s != want {
			t.Errorf("%s: got %q, want %q", name, s, want)
		}
	}
}

func TestParseProbe(t *testing.T) {
	out := []byte(`{"format":{"duration":"12.5","tags":{"creation_time":"2024-07-14T14:30:12.000000Z",
		"com.apple.quicktime.creationdate":"2024-07-14T15:30:12+0100","com.apple.quicktime.make":"Apple",
		"com.apple.quicktime.model":"iPhone 15","com.apple.quicktime.location.ISO6709":"+38.7223-009.1393+045.000/"}},
		"streams":[{"codec_type":"video","width":1920,"height":1080,"side_data_list":[{"rotation":-90}]}]}`)
	m, err := parseProbe(out, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if m.Taken.Format("2006-01-02 15:04:05") != "2024-07-14 15:30:12" || m.Duration != 12.5 || m.Width != 1080 || m.Height != 1920 ||
		m.Make != "Apple" || !m.HasGPS || m.Lat != 38.7223 || m.Lon != -9.1393 {
		t.Errorf("got %+v", m)
	}
}

func TestScaledTurns(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255}) // top left is red
	out := scaled(img, 100, 6)                // turn a quarter clockwise
	if b := out.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("size = %v", b)
	}
	// Turned clockwise, the top left goes to the top right.
	if c := out.RGBAAt(19, 0); c.R != 255 {
		t.Errorf("top right = %v", c)
	}
}

func TestFindEvents(t *testing.T) {
	day := func(d, h int) int64 { return time.Date(2024, 7, d, h, 0, 0, 0, time.UTC).Unix() }
	var items []store.HomeItem
	id := int64(0)
	add := func(n int, at int64, place string, lat, lon float64) {
		for i := range n {
			id++
			items = append(items, store.HomeItem{ID: id, Taken: at + int64(i*60), Place: place, Lat: lat, Lon: lon, HasGPS: place != ""})
		}
	}
	// Home: lots of photos over time.
	add(20, day(1, 10), "Leeds, United Kingdom", 53.8, -1.55)
	// A trip: three days in Lisbon, each a morning's photos.
	add(5, day(10, 9), "Lisbon, Portugal", 38.72, -9.14)
	add(5, day(11, 9), "Lisbon, Portugal", 38.72, -9.14)
	add(5, day(12, 9), "Sintra, Portugal", 38.8, -9.38)
	// A party at home.
	add(10, day(20, 19), "Leeds, United Kingdom", 53.8, -1.55)
	// A few photos, too few for an event.
	add(3, day(25, 12), "", 0, 0)
	evs := findEvents(items)
	if len(evs) != 3 {
		t.Fatalf("got %d events: %+v", len(evs), evs)
	}
	if len(evs[1].Items) != 15 || evs[1].Place != "Lisbon, Portugal" {
		t.Errorf("trip = %d items at %q", len(evs[1].Items), evs[1].Place)
	}
}

func TestScan(t *testing.T) {
	ctx := context.Background()
	st := storetest.Open(t)
	if err := mediatypes.SetEnabled(ctx, st, mediatypes.Home, true); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, err := st.AddRootFolder(ctx, mediatypes.Home, root); err != nil {
		t.Fatal(err)
	}
	towns := filepath.Join(t.TempDir(), "towns.txt")
	os.WriteFile(towns, []byte("2267057\tLisbon\tLisbon\t\t38.71667\t-9.13333\tP\tPPLC\tPT\t\t14\t\t\t\t517802\n"), 0o644)
	s := &Service{Store: st, Dir: t.TempDir(), Places: towns, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Recycle: func(root, path string) (string, error) {
			dst := filepath.Join(root, ".recycle", filepath.Base(path))
			os.MkdirAll(filepath.Dir(dst), 0o755)
			return dst, os.Rename(path, dst)
		}}
	write := func(rel string, b []byte) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Ten photos of a day in Lisbon, one of them saved twice.
	for i := range 10 {
		b := photo(t, 320, 240, uint8(i*25), exifBlock(t, "Canon", "EOS R6", 1, time.Date(2024, 7, 14, 10, i, 0, 0, time.UTC).Format("2006:01:02 15:04:05"), 38.7223, -9.1393))
		write(filepath.Join("2024", "Lisbon", "IMG_"+string(rune('0'+i))+".jpg"), b)
		if i == 3 {
			write(filepath.Join("Backup", "copy.jpg"), b)
		}
	}
	// One with no EXIF, dated by its name, and a hidden folder left out.
	write("PXL_20200101_120000.jpg", photo(t, 100, 100, 50, nil))
	write(".thumbnails/x.jpg", photo(t, 10, 10, 0, nil))
	write("notes.txt", []byte("hi"))

	run := func() Progress {
		t.Helper()
		s.progress = Progress{}
		if err := s.scan(ctx); err != nil {
			t.Fatal(err)
		}
		return s.Progress()
	}
	p := run()
	tot, _ := st.HomeTotals(ctx)
	if tot.Photos != 12 {
		t.Fatalf("photos = %d, progress %+v", tot.Photos, p)
	}
	all, _ := st.AllHomeItems(ctx)
	byName := map[string]store.HomeItem{}
	for _, it := range all {
		byName[filepath.Base(it.Path)] = it
		if it.Thumb != 1 {
			t.Errorf("%s has no thumbnail", it.Path)
		}
	}
	if it := byName["IMG_3.jpg"]; it.Place != "Lisbon, Portugal" || it.TakenFrom != "camera" || it.Make != "Canon" || it.Width != 320 {
		t.Errorf("IMG_3 = %+v", it)
	}
	if it := byName["PXL_20200101_120000.jpg"]; it.TakenFrom != "name" || time.Unix(it.Taken, 0).UTC().Year() != 2020 {
		t.Errorf("PXL = %+v", it)
	}
	// The day out is an event album of 11 (with the copy).
	albums, _ := st.HomeAlbums(ctx, false)
	if len(albums) != 1 || albums[0].Count != 11 || albums[0].Place != "Lisbon, Portugal" || albums[0].Kind != "event" {
		t.Fatalf("albums = %+v", albums)
	}
	// Naming it keeps the name when scanned again.
	name := "Summer in Lisbon"
	st.EditHomeAlbum(ctx, albums[0].ID, store.HomeAlbumEdit{Name: &name})
	// The copy is found.
	d, _ := s.Dupes(ctx)
	if len(d) != 1 || d[0].Kind != "same" || len(d[0].Items) != 2 {
		t.Fatalf("dupes = %+v", d)
	}
	spare := d[0].Items[1]
	// Moving both isn't allowed; moving the spare one is.
	if _, _, err := s.RecycleCopies(ctx, []int64{d[0].Items[0].ID, spare.ID}, d[0].Items[0].Size+spare.Size); err == nil {
		t.Error("moved every copy")
	}
	if n, _, err := s.RecycleCopies(ctx, []int64{spare.ID}, spare.Size); err != nil || n != 1 {
		t.Fatalf("recycle = %d %v", n, err)
	}
	if _, err := os.Stat(spare.Path); !os.IsNotExist(err) {
		t.Error("the copy is still there")
	}
	// Scanning again reads nothing new and keeps the album's name.
	p = run()
	if p.New != 0 {
		t.Errorf("new on second scan = %d", p.New)
	}
	albums, _ = st.HomeAlbums(ctx, false)
	if len(albums) != 1 || albums[0].Name != name || albums[0].Count != 10 {
		t.Errorf("albums after = %+v", albums)
	}
	// A root folder that looks empty forgets nothing.
	os.RemoveAll(root)
	os.MkdirAll(root, 0o755)
	p = run()
	tot, _ = st.HomeTotals(ctx)
	if tot.Photos != 11 || p.Note == "" {
		t.Errorf("after the folder emptied: %d photos, note %q", tot.Photos, p.Note)
	}
}

// A video's facts come from ffprobe and its thumbnail from FFmpeg.
func TestReadVideo(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "VID_20230102_101112.mp4")
	if out, err := exec.Command(ff, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=3",
		"-metadata", "creation_time=2023-01-02T10:11:12Z", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
		t.Skipf("can't make a test video: %v %s", err, out)
	}
	s := &Service{Dir: t.TempDir(), Places: "off", Location: time.UTC}
	fi, _ := os.Stat(clip)
	it, err := s.read(context.Background(), job{root: dir, path: clip, size: fi.Size(), mtime: fi.ModTime().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if it.Kind != "video" || it.Width != 320 || it.Height != 240 || it.Duration < 2.9 || it.TakenFrom != "video" ||
		time.Unix(it.Taken, 0).UTC().Format("2006-01-02 15:04") != "2023-01-02 10:11" || it.Thumb != 1 {
		t.Fatalf("got %+v", it)
	}
	if _, err := s.Thumb(it); err != nil {
		t.Fatal(err)
	}
}
