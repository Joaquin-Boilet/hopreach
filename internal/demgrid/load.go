//go:build !js

package demgrid

// Scratch allocation for the DEM grid lives in platform files
// (mmap_unix.go / mmap_windows.go): Linux m-maps a temp file so a
// whole-region grid can page under a container's memory ceiling; Windows
// plain-allocates (a dev-host build, not a deployment target).

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Load ensures every tile covering b is cached on disk, downloading any
// that are missing (with limited concurrency), then decodes them into a
// single in-memory grid. progress is called with (done, total) as tiles are
// resolved (cache hit or download).
func Load(b Bounds, zoom int, cacheDir, tileURLBase string, client *http.Client, progress func(done, total int)) (*Grid, error) {
	minTileX := int(math.Floor(lonToTileX(b.West, zoom)))
	maxTileX := int(math.Floor(lonToTileX(b.East, zoom)))
	minTileY := int(math.Floor(latToTileY(b.North, zoom)))
	maxTileY := int(math.Floor(latToTileY(b.South, zoom)))

	tilesWide := maxTileX - minTileX + 1
	tilesHigh := maxTileY - minTileY + 1
	if tilesWide <= 0 || tilesHigh <= 0 {
		return nil, fmt.Errorf("demgrid: empty tile range for bounds %+v", b)
	}

	total := tilesWide * tilesHigh
	grid := &Grid{
		Zoom:      zoom,
		MinTileX:  minTileX,
		MinTileY:  minTileY,
		TilesWide: tilesWide,
		TilesHigh: tilesHigh,
		Width:     tilesWide * tileSize,
		Height:    tilesHigh * tileSize,
	}
	elev, release, err := mmapFloat32(filepath.Join(cacheDir, "grid-scratch"), grid.Width*grid.Height)
	if err != nil {
		return nil, fmt.Errorf("demgrid: allocating grid: %w", err)
	}
	grid.Elev = elev
	grid.release = release

	type job struct{ tx, ty int }
	jobs := make(chan job, total)
	for ty := minTileY; ty <= maxTileY; ty++ {
		for tx := minTileX; tx <= maxTileX; tx++ {
			jobs <- job{tx, ty}
		}
	}
	close(jobs)

	var mu sync.Mutex
	done := 0
	var wg sync.WaitGroup
	const workers = 16
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				elev, err := fetchTile(j.tx, j.ty, zoom, cacheDir, tileURLBase, client)
				if err != nil {
					log.Printf("demgrid: tile %d/%d/%d failed, treating as sea level: %v", zoom, j.tx, j.ty, err)
					elev = nil // leave as zero-filled
				}
				if elev != nil {
					ox := (j.tx - minTileX) * tileSize
					oy := (j.ty - minTileY) * tileSize
					for row := 0; row < tileSize; row++ {
						copy(grid.Elev[(oy+row)*grid.Width+ox:(oy+row)*grid.Width+ox+tileSize], elev[row*tileSize:(row+1)*tileSize])
					}
				}
				mu.Lock()
				done++
				if progress != nil {
					progress(done, total)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	return grid, nil
}

// fetchTile returns a tileSize*tileSize row-major elevation slice, using the
// on-disk cache when present.
func fetchTile(tx, ty, zoom int, cacheDir, tileURLBase string, client *http.Client) ([]float32, error) {
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%d", zoom), fmt.Sprintf("%d", tx), fmt.Sprintf("%d.png", ty))

	if data, err := os.ReadFile(cachePath); err == nil {
		return decodeTerrarium(data)
	}

	url := fmt.Sprintf("%s/%d/%d/%d.png", tileURLBase, zoom, tx, ty)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return nil, err
	}
	tmp := cachePath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(io.TeeReader(resp.Body, f))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		return nil, err
	}

	return terrariumFromImage(img)
}

func decodeTerrarium(data []byte) ([]float32, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return terrariumFromImage(img)
}

// terrariumFromImage decodes a terrarium-encoded tile image into a
// tileSize*tileSize row-major elevation slice (metres).
func terrariumFromImage(img image.Image) ([]float32, error) {
	b := img.Bounds()
	if b.Dx() != tileSize || b.Dy() != tileSize {
		return nil, fmt.Errorf("unexpected tile size %dx%d", b.Dx(), b.Dy())
	}
	out := make([]float32, tileSize*tileSize)
	for y := 0; y < tileSize; y++ {
		for x := 0; x < tileSize; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// image.Color.RGBA() returns 16-bit-scaled channels; scale back to 8-bit.
			r8 := float64(r >> 8)
			g8 := float64(g >> 8)
			b8 := float64(bl >> 8)
			out[y*tileSize+x] = float32((r8*256 + g8 + b8/256) - 32768)
		}
	}
	return out, nil
}
