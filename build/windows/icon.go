package main

import (
	"image"
	"image/color"

	"github.com/tc-hib/winres"
	"golang.org/x/image/vector"
)

// appIcon renders the same mark as frontend/public/favicon.svg. It is embedded
// as EXE resource 1 and loaded by the tray without extracting temporary files.
func appIcon() (*winres.Icon, error) {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	path := vector.NewRasterizer(size, size)
	scale := float32(size) / 48
	move := func(x, y float32) { path.MoveTo(x*scale, y*scale) }
	line := func(x, y float32) { path.LineTo(x*scale, y*scale) }
	quad := func(x, y, endX, endY float32) { path.QuadTo(x*scale, y*scale, endX*scale, endY*scale) }
	fill := func(c color.Color) {
		path.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{})
		path.Reset(size, size)
	}
	move(12, 0)
	line(36, 0)
	quad(48, 0, 48, 12)
	line(48, 36)
	quad(48, 48, 36, 48)
	line(12, 48)
	quad(0, 48, 0, 36)
	line(0, 12)
	quad(0, 0, 12, 0)
	path.ClosePath()
	fill(color.RGBA{49, 92, 234, 255})
	move(12, 34)
	line(22, 12)
	line(27, 12)
	line(36, 34)
	line(30, 34)
	line(28, 28)
	line(20, 28)
	line(18, 34)
	path.ClosePath()
	// Counter-wound inner triangle keeps the A's opening transparent.
	move(22, 23)
	line(26, 23)
	line(24, 17)
	path.ClosePath()
	fill(color.White)
	move(33, 7)
	line(41, 7)
	line(41, 15)
	line(39, 15)
	line(39, 10.4)
	line(31.7, 17.7)
	line(30.3, 16.3)
	line(37.6, 9)
	line(33, 9)
	path.ClosePath()
	fill(color.White)
	return winres.NewIconFromResizedImage(img, []int{256, 64, 48, 32, 24, 16})
}
