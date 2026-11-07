//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// lockPath is process-wide (not per-config), deliberately: the fetch/compute
// pipeline can be triggered from several independent places and two racing
// writers would silently corrupt the output rather than fail loudly.
// Windows dev-host build: use the user temp dir, /tmp does not exist here.
const lockPath = "hopreach.lock" // resolved against os.TempDir() below

// LockFileEx lives in kernel32; keep the dependency count at zero by
// declaring it directly (golang.org/x/sys/windows is not otherwise in
// this project's module graph).
var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

const (
	lockfileExclusiveLockFlag = 0x2
	lockfileFailImmediately   = 0x1
)

// overlapped mirrors the OVERLAPPED structure LockFileEx needs. The whole
// file is locked with offset 0/len 1 — a byte-range lock on an exclusively
// used sentinel file works exactly like the Unix flock contract here.
type overlapped struct {
	Internal, InternalHigh uintptr
	Offset, OffsetHigh     uint32
	Handle                 syscall.Handle
}

func lockPathResolved() string {
	return os.TempDir() + "\\" + lockPath
}

// acquireLock takes a non-blocking exclusive lock, auto-releasing if this
// process dies (Windows drops file locks at process exit, same guarantee
// flock gives on Unix), erroring immediately when another run holds it.
func acquireLock() (*os.File, error) {
	f, err := os.OpenFile(lockPathResolved(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("abriendo lock file: %w", err)
	}
	var ov overlapped
	flags := uintptr(lockfileExclusiveLockFlag | lockfileFailImmediately)
	ok, _, callErr := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if ok == 0 {
		f.Close()
		if callErr != nil && callErr.Error() != syscall.EINVAL.Error() {
			return nil, fmt.Errorf("lock: %w", callErr)
		}
		return nil, fmt.Errorf("another run already in progress")
	}
	return f, nil
}
