package homemedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoded for thumbnails
	"image/jpeg"
	_ "image/png" // decoded for thumbnails
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Sizes of what's made from each photo and video.
const (
	thumbSize   = 480  // the longest side of a thumbnail, for the grid
	previewSize = 2048 // a viewable copy of a photo browsers can't show
)

// scaled shrinks img to fit in size×size, turned the way orientation says
// (EXIF's 1-8), averaging the pixels each new one covers.
func scaled(img image.Image, size, orientation int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	// Orientations 5-8 turn it on its side.
	ow, oh := sw, sh
	if orientation >= 5 && orientation <= 8 {
		ow, oh = sh, sw
	}
	w, h := ow, oh
	if w > size || h > size {
		if w >= h {
			w, h = size, max(1, oh*size/ow)
		} else {
			w, h = max(1, ow*size/oh), size
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	// Up to 3×3 samples for each new pixel.
	n := 3
	if ow <= w*2 {
		n = 1
	}
	for y := range h {
		for x := range w {
			var r, g, bl, a uint32
			for j := range n {
				for i := range n {
					// The point in the turned picture...
					fx := (float64(x) + (float64(i)+0.5)/float64(n)) * float64(ow) / float64(w)
					fy := (float64(y) + (float64(j)+0.5)/float64(n)) * float64(oh) / float64(h)
					px, py := min(int(fx), ow-1), min(int(fy), oh-1)
					// ...and where it is in the file's.
					sx, sy := unturn(px, py, ow, oh, orientation)
					cr, cg, cb, ca := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
				}
			}
			d := uint32(n * n)
			out.SetRGBA(x, y, color.RGBA{uint8(r / d >> 8), uint8(g / d >> 8), uint8(bl / d >> 8), uint8(a / d >> 8)})
		}
	}
	return out
}

// unturn maps a pixel of the picture as shown (ow×oh) back to the file's.
func unturn(x, y, ow, oh, orientation int) (int, int) {
	switch orientation {
	case 2:
		return ow - 1 - x, y
	case 3:
		return ow - 1 - x, oh - 1 - y
	case 4:
		return x, oh - 1 - y
	case 5:
		return y, x
	case 6:
		return y, ow - 1 - x
	case 7:
		return oh - 1 - y, ow - 1 - x
	case 8:
		return oh - 1 - y, x
	}
	return x, y
}

// shown is a picture's size once turned the way orientation says.
func shown(w, h, orientation int) (int, int) {
	if orientation >= 5 && orientation <= 8 {
		return h, w
	}
	return w, h
}

// writeJPEG saves a picture, whole or not at all.
func writeJPEG(path string, img image.Image, quality int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// decodeSmall decodes a picture Go can read (JPEG, PNG, GIF), refusing
// ones too big to hold in memory.
func decodeSmall(b []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > 120_000_000 {
		return nil, errors.New("too many pixels to read")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// ffmpegFrame makes a JPEG of a picture or a video's frame with FFmpeg,
// for what Go can't read: HEIC, WebP, TIFF, and every video. at is where
// in a video to take it from.
func ffmpegFrame(ctx context.Context, ffmpeg, in, out string, size int, at float64) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tmp := out + ".tmp.jpg"
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if at > 0 {
		args = append(args, "-ss", strconv.FormatFloat(at, 'f', 2, 64))
	}
	args = append(args, "-i", in, "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", size, size),
		"-q:v", "4", "-f", "image2", tmp)
	if b, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(tmp)
		msg := bytes.TrimSpace(b)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return os.Rename(tmp, out)
}

// lookOf is a picture's difference hash: 64 bits saying, row by row of a
// 9×8 grey copy, whether each pixel is brighter than the next. Pictures
// that look alike, such as a photo and a smaller copy of it, have hashes
// a few bits apart.
func lookOf(img image.Image) string {
	b := img.Bounds()
	// Each of the 9×8 cells is the average of every pixel in it, so grain
	// and noise even out.
	var sum [8][9]float64
	var n [8][9]int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		cy := (y - b.Min.Y) * 8 / b.Dy()
		for x := b.Min.X; x < b.Max.X; x++ {
			cx := (x - b.Min.X) * 9 / b.Dx()
			r, g, bl, _ := img.At(x, y).RGBA()
			sum[cy][cx] += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
			n[cy][cx]++
		}
	}
	var grey [8][9]float64
	for y := range 8 {
		for x := range 9 {
			if n[y][x] > 0 {
				grey[y][x] = sum[y][x] / float64(n[y][x])
			}
		}
	}
	// Only a clear step counts as brighter, so a plain sky, where cells
	// are all but equal, reads the same in every copy of it.
	const step = 2 * 257 // two shades of 255, in 16-bit colour
	var h uint64
	for y := range 8 {
		for x := range 8 {
			h <<= 1
			if grey[y][x] > grey[y][x+1]+step {
				h |= 1
			}
		}
	}
	return fmt.Sprintf("%016x", h)
}

// lookDistance is how many bits two looks differ by; 64 if one is missing.
func lookDistance(a, b string) int {
	x, err1 := strconv.ParseUint(a, 16, 64)
	y, err2 := strconv.ParseUint(b, 16, 64)
	if err1 != nil || err2 != nil || a == "" || b == "" {
		return 64
	}
	return bits.OnesCount64(x ^ y)
}

// flat says whether a look is of a picture with almost nothing in it, such
// as a black frame, which looks like every other one.
func flat(look string) bool {
	x, err := strconv.ParseUint(look, 16, 64)
	if err != nil {
		return true
	}
	n := bits.OnesCount64(x)
	return n < 4 || n > 60
}
