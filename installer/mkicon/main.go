// mkicon makes the program's icon, icon.ico, from the MCS logo, logo.png:
// the middle of the logo on a dark tile with rounded corners, at every size
// Windows asks for. It also makes installer/wizard.bmp, the same picture for
// the installer's pages. Run it from the repository's folder, then make
// rsrc.syso again so the exe carries the new icon (see the README):
//
//	go run ./installer/mkicon
//
// The whole logo is too wide and too detailed for an icon, so its sides (the
// flags) are left off and the middle fills the tile.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	master = 1024 // the icon is drawn this big, then shrunk to each size
	corner = 0.20 // the tile's corner radius, as a share of its width
	rim    = 0.03 // the yellow line round the tile, likewise
	gap    = 0.02 // between the yellow line and the logo, likewise
)

// rimFor is the width of the yellow line in an icon n pixels square, as a
// share of n: never less than a whole pixel, or at the small sizes it would
// fade away.
func rimFor(n int) float64 { return math.Max(rim, 1/float64(n)) }

var (
	tileTop    = color.NRGBA{0x2b, 0x2e, 0x36, 0xff}
	tileBottom = color.NRGBA{0x0d, 0x0e, 0x11, 0xff}
	rimColour  = color.NRGBA{0xf6, 0xc6, 0x0a, 0xff} // the logo's yellow
)

// The sizes in the icon, in pixels. Windows picks the nearest: 16 to 24 for
// title bars and lists, 32 to 48 for the taskbar and Start menu, 64 and up
// for desktop shortcuts on sharp screens.
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

func main() {
	if err := run("logo.png", "icon.ico"); err != nil {
		fmt.Fprintln(os.Stderr, "mkicon:", err)
		os.Exit(1)
	}
}

func run(logoFile, iconFile string) error {
	f, err := os.Open(logoFile)
	if err != nil {
		return err
	}
	defer f.Close()
	logo, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("%s: %w", logoFile, err)
	}
	var images [][]byte
	for _, n := range sizes {
		small := shrink(drawMaster(logo, rimFor(n)), n)
		if n >= 256 {
			var buf bytes.Buffer
			if err := png.Encode(&buf, unmultiply(small)); err != nil {
				return err
			}
			images = append(images, buf.Bytes())
		} else {
			images = append(images, bitmap(small))
		}
	}
	if err := os.WriteFile(iconFile, icoFile(sizes, images), 0644); err != nil {
		return err
	}
	// The same picture for the top corner of the installer's pages.
	return os.WriteFile(wizardFile, whiteBitmap(shrink(drawMaster(logo, rimFor(wizardSize)), wizardSize)), 0644)
}

// The picture at the top right of the installer's pages, which are white.
// The installer scales it to fit.
const (
	wizardFile = "installer/wizard.bmp"
	wizardSize = 165
)

// whiteBitmap is the image on a white background as a .bmp file.
func whiteBitmap(img *image.RGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	row := (w*3 + 3) / 4 * 4
	var buf bytes.Buffer
	buf.WriteString("BM")
	binary.Write(&buf, binary.LittleEndian, struct {
		FileSize, Reserved, Offset uint32
		Size                       uint32
		Width, Height              int32
		Planes, Bits               uint16
		Compression, ImageSize     uint32
		XPerM, YPerM               int32
		Used, Needed               uint32
	}{FileSize: uint32(54 + row*h), Offset: 54, Size: 40, Width: int32(w), Height: int32(h), Planes: 1, Bits: 24, ImageSize: uint32(row * h)})
	for y := h - 1; y >= 0; y-- {
		line := make([]byte, row)
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y) // multiplied by its alpha, so white shows through what is clear
			white := 255 - c.A
			line[x*3], line[x*3+1], line[x*3+2] = c.B+white, c.G+white, c.R+white
		}
		buf.Write(line)
	}
	return buf.Bytes()
}

// drawMaster draws the icon at its largest: the tile, and the middle of the
// logo on it. rim is the width of the yellow line, as a share of the tile's.
// Colours are held multiplied by their alpha, as image.RGBA does.
func drawMaster(logo image.Image, rim float64) *image.RGBA {
	margin := rim + gap
	img := image.NewRGBA(image.Rect(0, 0, master, master))
	radius := corner * master
	for y := 0; y < master; y++ {
		shade := float64(y) / (master - 1)
		fill := mix(tileTop, tileBottom, shade)
		for x := 0; x < master; x++ {
			// How far inside the tile's edge this pixel is.
			in := insideRoundedSquare(float64(x)+0.5, float64(y)+0.5, master, radius)
			if in <= -0.5 {
				continue
			}
			c := fill
			if in < rim*master {
				c = rimColour
			}
			a := math.Min(1, in+0.5) // a soft edge one pixel wide
			img.SetRGBA(x, y, color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(255 * a)})
		}
	}

	// The middle of the logo: a square as tall as the logo is.
	b := logo.Bounds()
	side := b.Dy()
	left := b.Min.X + (b.Dx()-side)/2
	box := master * (1 - 2*margin)
	scale := float64(side) / box
	for y := 0; y < master; y++ {
		for x := 0; x < master; x++ {
			in := insideRoundedSquare(float64(x)+0.5, float64(y)+0.5, master, radius)
			if in < rim*master {
				continue // the logo stays inside the yellow line
			}
			sx := (float64(x)+0.5-margin*master)*scale - 0.5
			sy := (float64(y)+0.5-margin*master)*scale - 0.5
			if sx < -1 || sy < -1 || sx > float64(side) || sy > float64(side) {
				continue
			}
			r, g, bl, a := sample(logo, left, b.Min.Y, side, sx, sy)
			if a == 0 {
				continue
			}
			// Over what is already there.
			under := img.RGBAAt(x, y)
			keep := 1 - a
			img.SetRGBA(x, y, color.RGBA{
				uint8(math.Min(255, r*255+float64(under.R)*keep)),
				uint8(math.Min(255, g*255+float64(under.G)*keep)),
				uint8(math.Min(255, bl*255+float64(under.B)*keep)),
				uint8(math.Min(255, a*255+float64(under.A)*keep)),
			})
		}
	}
	return img
}

// insideRoundedSquare is how far the point is inside a square of the given
// size with rounded corners: negative outside it.
func insideRoundedSquare(x, y, size, radius float64) float64 {
	// Distance from the centre, folded into one corner.
	dx := math.Abs(x-size/2) - (size/2 - radius)
	dy := math.Abs(y-size/2) - (size/2 - radius)
	outside := math.Hypot(math.Max(dx, 0), math.Max(dy, 0))
	inside := math.Min(math.Max(dx, dy), 0)
	return radius - (outside + inside)
}

func mix(a, b color.NRGBA, t float64) color.NRGBA {
	at := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.NRGBA{at(a.R, b.R), at(a.G, b.G), at(a.B, b.B), 0xff}
}

// sample reads the logo at a point between its pixels, blending the four
// around it. The result is multiplied by its alpha, each part 0 to 1. Points
// off the square read as clear.
func sample(logo image.Image, left, top, side int, x, y float64) (r, g, b, a float64) {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	for _, p := range []struct {
		dx, dy int
		w      float64
	}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		px, py := x0+p.dx, y0+p.dy
		if px < 0 || py < 0 || px >= side || py >= side || p.w == 0 {
			continue
		}
		cr, cg, cb, ca := logo.At(left+px, top+py).RGBA() // already multiplied by alpha
		r += float64(cr) / 0xffff * p.w
		g += float64(cg) / 0xffff * p.w
		b += float64(cb) / 0xffff * p.w
		a += float64(ca) / 0xffff * p.w
	}
	return
}

// shrink makes the master n pixels square, each pixel the average of the
// ones it covers.
func shrink(big *image.RGBA, n int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, n, n))
	step := float64(big.Bounds().Dx()) / float64(n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var sum [4]float64
			var weight float64
			y0, y1 := float64(y)*step, float64(y+1)*step
			x0, x1 := float64(x)*step, float64(x+1)*step
			for sy := int(y0); float64(sy) < y1; sy++ {
				wy := math.Min(y1, float64(sy+1)) - math.Max(y0, float64(sy))
				for sx := int(x0); float64(sx) < x1; sx++ {
					w := wy * (math.Min(x1, float64(sx+1)) - math.Max(x0, float64(sx)))
					c := big.RGBAAt(sx, sy)
					sum[0] += float64(c.R) * w
					sum[1] += float64(c.G) * w
					sum[2] += float64(c.B) * w
					sum[3] += float64(c.A) * w
					weight += w
				}
			}
			out.SetRGBA(x, y, color.RGBA{
				uint8(sum[0]/weight + 0.5), uint8(sum[1]/weight + 0.5),
				uint8(sum[2]/weight + 0.5), uint8(sum[3]/weight + 0.5),
			})
		}
	}
	return out
}

// unmultiply gives the image with its colours no longer multiplied by their
// alpha, which is how icons and PNG files hold them.
func unmultiply(img *image.RGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			out.Set(x, y, color.NRGBAModel.Convert(img.RGBAAt(x, y)))
		}
	}
	return out
}

// bitmap is the image as an icon holds its smaller sizes: a Windows bitmap
// without its file header, bottom row first, followed by a one-bit mask of
// the clear pixels.
func bitmap(img *image.RGBA) []byte {
	n := img.Bounds().Dx()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, struct {
		Size          uint32
		Width, Height int32
		Planes, Bits  uint16
		Compression   uint32
		ImageSize     uint32
		XPerM, YPerM  int32
		Used, Needed  uint32
	}{Size: 40, Width: int32(n), Height: int32(2 * n), Planes: 1, Bits: 32, ImageSize: uint32(n * n * 4)})
	plain := unmultiply(img)
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := plain.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	rowBytes := (n + 31) / 32 * 4
	for y := n - 1; y >= 0; y-- {
		row := make([]byte, rowBytes)
		for x := 0; x < n; x++ {
			if plain.NRGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		buf.Write(row)
	}
	return buf.Bytes()
}

// icoFile puts the images together as an .ico file.
func icoFile(sizes []int, images [][]byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	offset := 6 + 16*len(images)
	for i, data := range images {
		n := uint8(sizes[i]) // 256 is written as 0
		binary.Write(&buf, binary.LittleEndian, struct {
			Width, Height, Colours, Reserved uint8
			Planes, Bits                     uint16
			Size, Offset                     uint32
		}{n, n, 0, 0, 1, 32, uint32(len(data)), uint32(offset)})
		offset += len(data)
	}
	for _, data := range images {
		buf.Write(data)
	}
	return buf.Bytes()
}
