package emit

import (
	"bytes"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"

	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
)

// EmitGIF renders a sequence of scenes into an animated GIF.
// delayCS specifies the frame delay in centiseconds (100ths of a second, e.g. 10 = 100ms).
func EmitGIF(scenes []*scene.Scene, delayCS int, opts RasterOptions) ([]byte, error) {
	if len(scenes) == 0 {
		return nil, fmt.Errorf("no scenes provided for animated gif")
	}
	if delayCS <= 0 {
		delayCS = 50 // default 500ms
	}

	outGIF := &gif.GIF{
		Image:     make([]*image.Paletted, len(scenes)),
		Delay:     make([]int, len(scenes)),
		LoopCount: 0, // infinite loop
	}

	for i, sc := range scenes {
		rgba, err := Rasterize(sc, opts)
		if err != nil {
			return nil, fmt.Errorf("rasterize frame %d: %w", i, err)
		}

		paletted := image.NewPaletted(rgba.Bounds(), palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, rgba.Bounds(), rgba, image.Point{})

		outGIF.Image[i] = paletted
		outGIF.Delay[i] = delayCS
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, outGIF); err != nil {
		return nil, fmt.Errorf("encode animated gif: %w", err)
	}

	return buf.Bytes(), nil
}
