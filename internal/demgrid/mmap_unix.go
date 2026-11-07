//go:build !js && !windows

package demgrid

// The Unix scratch allocation: a temp file memory-mapped MAP_SHARED.
// Windows uses the plain-heap fallback in mmap_windows.go — see both
// doc comments for why they differ.

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// mmapFloat32 allocates n float32s backed by a temp file memory-mapped
// MAP_SHARED, returning the slice and a cleanup func that unmaps and
// deletes the backing file. The returned slice behaves like a normal Go
// slice for every existing read/write call site (Grid.At, tile decode
// writes) — only the allocation strategy changes.
// scratchDir is where the file lives — this matters: it must be genuinely
// disk-backed. The OS default temp directory is a tempting default but is
// not safe to assume here — it's tmpfs (RAM-backed) on some hosts, which
// would make writes to it consume real memory identically to a plain heap
// allocation, defeating the entire point. Callers pass the DEM tile cache
// directory instead, which is already required to be a real (typically
// Docker-volume-backed) disk location for tile persistence across runs.
func mmapFloat32(scratchDir string, n int) ([]float32, func() error, error) {
	if n <= 0 {
		return nil, nil, fmt.Errorf("demgrid: invalid grid size %d", n)
	}
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("demgrid: scratch dir: %w", err)
	}
	f, err := os.CreateTemp(scratchDir, "hopreach-dem-grid-*.bin")
	if err != nil {
		return nil, nil, fmt.Errorf("demgrid: create scratch file: %w", err)
	}
	path := f.Name()
	size := int64(n) * 4
	if err := f.Truncate(size); err != nil {
		f.Close()
		os.Remove(path)
		return nil, nil, fmt.Errorf("demgrid: truncate scratch file: %w", err)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	f.Close() // the mapping itself keeps the underlying file usable; the fd isn't needed once mmap'd
	if err != nil {
		os.Remove(path)
		return nil, nil, fmt.Errorf("demgrid: mmap: %w", err)
	}
	elev := unsafe.Slice((*float32)(unsafe.Pointer(&data[0])), n)
	release := func() error {
		err := syscall.Munmap(data)
		if rmErr := os.Remove(path); err == nil {
			err = rmErr
		}
		return err
	}
	return elev, release, nil
}
