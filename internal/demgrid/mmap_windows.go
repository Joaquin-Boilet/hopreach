//go:build !js && windows

package demgrid

// Windows fallback for the DEM grid's scratch allocation: a plain heap
// slice. Unix demands the mmap dance because a whole-region grid at the
// Precision tier's DEM zoom runs to several GB and must be able to page
// under a container's memory ceiling; on Windows — a dev-host build, not a
// deployment target — plain allocation is fine, and Go's GC handles the
// swap. Release drops the slice; the file did not exist, so nothing to
// unlink, and the caller's cache metadata is unaffected (the scratch file
// is transparent to every caller — see load.go's use).

import "os"

func mmapFloat32(scratchDir string, n int) ([]float32, func() error, error) {
	if n <= 0 {
		return nil, nil, os.ErrInvalid
	}
	return make([]float32, n), func() error { return nil }, nil
}
