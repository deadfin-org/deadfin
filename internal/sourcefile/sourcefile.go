// SPDX-License-Identifier: GPL-2.0-only

// Package sourcefile pins one media source file behind a descriptor. The path
// is used to open once and afterwards only for liveness re-checks; every fact
// about the processed bytes is proven against the retained descriptor.
package sourcefile

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ErrStaleSource reports that a source no longer matches, or never matched,
// the file facts from its database snapshot.
var ErrStaleSource = errors.New("source file does not match its snapshot")

// Expected holds the snapshot facts that an opened source must match. Nil
// fields are unknown and are not checked.
type Expected struct {
	Size       *int64
	ModifiedAt *time.Time
	Inode      *int64
	ChangedAt  *time.Time
}

// Source owns a pinned regular media-file descriptor. File returns a borrowed
// descriptor that remains valid until Close; callers must not close it.
type Source struct {
	mu   sync.RWMutex
	file *os.File
	fd   int
	path string

	dev, ino uint64
	size     int64
	mtime    unix.Timespec
	ctime    unix.Timespec
	// nlink is the only open-time fact that a pathname replacement reliably
	// disturbs on every platform. POSIX requires unlink() to update the file's
	// last-status-change timestamp only "if the file's link count is not 0", so
	// when a rename drops the pinned inode's last link, Linux leaves ctime
	// untouched while macOS bumps it. Measured on both: size, mtime and ctime
	// are all unchanged on Linux after a rename-over, and only nlink moves.
	nlink uint64

	viaSymlink       bool
	linkDev, linkIno uint64
}

// Open opens path once, validates the opened descriptor against expected, and
// retains that descriptor. Final symlinks are deliberately followed: the
// library scanner records the facts of a link's target, as upstream does
// (ManagedFileSystem.GetFileSystemMetadata), so expected facts describe the
// regular-file target that the descriptor pins. The link's own identity is
// kept for the liveness re-checks.
func Open(path string, expected Expected) (*Source, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		if isMissing(err) {
			return nil, stale("open source %q: %v", path, err)
		}
		return nil, fmt.Errorf("open source %q: %w", path, err)
	}

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("fstat source %q: %w", path, err))
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, closeOpenFD(fd, stale("source %q is not a regular file", path))
	}

	// O_NONBLOCK protects the open of a FIFO/device. Clear it before exposing a
	// regular file to a consumer so descriptor semantics stay conventional.
	status, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("get source status flags %q: %w", path, err))
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, status&^unix.O_NONBLOCK); err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("clear source non-blocking flag %q: %w", path, err))
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		return nil, closeOpenFD(fd, fmt.Errorf("wrap source descriptor %q", path))
	}

	var listed unix.Stat_t
	if err := unix.Lstat(path, &listed); err != nil {
		return nil, closeOpenFile(file, pathLstatError(path, err))
	}

	mtime, ctime := statTimes(&opened)
	source := &Source{
		file:  file,
		fd:    fd,
		path:  path,
		dev:   uint64(opened.Dev),
		ino:   uint64(opened.Ino),
		size:  opened.Size,
		mtime: mtime,
		ctime: ctime,
		nlink: uint64(opened.Nlink),
	}

	if listed.Mode&unix.S_IFMT == unix.S_IFLNK {
		source.viaSymlink = true
		source.linkDev = uint64(listed.Dev)
		source.linkIno = uint64(listed.Ino)
	} else if uint64(listed.Dev) != source.dev || uint64(listed.Ino) != source.ino {
		return nil, closeOpenFile(file, stale("source %q changed while opening", path))
	}
	if err := verifyExpected(expected, source.size, source.ino, source.mtime, source.ctime); err != nil {
		return nil, closeOpenFile(file, fmt.Errorf("source %q: %w", path, err))
	}

	return source, nil
}

// Path returns the path used to open the pinned source.
func (s *Source) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// File returns the borrowed pinned descriptor. It is valid until Close and
// must not be closed by the caller.
func (s *Source) File() *os.File {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.file
}

// Rewind resets the pinned descriptor's offset to zero. Sequential children
// opening FDPath on Darwin share this offset, so a borrower must rewind before
// each child starts reading.
func (s *Source) Rewind() error {
	return s.withFD(func(fd int) error {
		_, err := unix.Seek(fd, 0, 0)
		return err
	})
}

// OpenAuxiliary independently opens the pinned source path and verifies that
// its descriptor still identifies the exact regular file and change facts
// captured by Open. The returned descriptor is caller-owned and must be
// closed by the caller.
func (s *Source) OpenAuxiliary() (*os.File, error) {
	if s == nil {
		return nil, errors.New("source is nil")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return nil, errors.New("source is closed")
	}

	fd, err := unix.Open(s.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		if isMissing(err) {
			return nil, stale("open auxiliary source %q: %v", s.path, err)
		}
		return nil, fmt.Errorf("open auxiliary source %q: %w", s.path, err)
	}

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("fstat auxiliary source %q: %w", s.path, err))
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, closeOpenFD(fd, stale("auxiliary source %q is not a regular file", s.path))
	}
	mtime, ctime := statTimes(&opened)
	if uint64(opened.Dev) != s.dev || uint64(opened.Ino) != s.ino || opened.Size != s.size || !sameTimespec(mtime, s.mtime) || !sameTimespec(ctime, s.ctime) {
		return nil, closeOpenFD(fd, stale("auxiliary source %q does not match pinned source", s.path))
	}

	// O_NONBLOCK protects the open of a FIFO/device. Clear it after validating
	// a regular file so the caller receives conventional descriptor semantics.
	status, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("get auxiliary source status flags %q: %w", s.path, err))
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, status&^unix.O_NONBLOCK); err != nil {
		return nil, closeOpenFD(fd, fmt.Errorf("clear auxiliary source non-blocking flag %q: %w", s.path, err))
	}

	file := os.NewFile(uintptr(fd), s.path)
	if file == nil {
		return nil, closeOpenFD(fd, fmt.Errorf("wrap auxiliary source descriptor %q", s.path))
	}
	return file, nil
}

// FDPath returns the seekable /dev/fd name for an inherited child descriptor.
// On Linux /dev/fd is a procfs magic-link re-open of the same inode; on Darwin
// devfs duplicates the descriptor. Independent consumers need separate parent
// open-file descriptions because Darwin duplicates share their file offset.
// The owner may Rewind between sequential child borrows; Source.OpenAuxiliary
// supplies a separately opened descriptor when concurrent consumers require it.
func FDPath(fd int) string {
	return fmt.Sprintf("/dev/fd/%d", fd)
}

// Fingerprint is the exact open-time identity and change facts for a pinned
// regular file.
type Fingerprint struct {
	Dev, Ino     uint64
	Size         int64
	MTime, CTime time.Time
}

// Fingerprint returns the open-time fstat facts for the pinned regular file.
func (s *Source) Fingerprint() Fingerprint {
	if s == nil {
		return Fingerprint{}
	}
	return Fingerprint{
		Dev:   s.dev,
		Ino:   s.ino,
		Size:  s.size,
		MTime: timeFromTimespec(s.mtime),
		CTime: timeFromTimespec(s.ctime),
	}
}

// Identity returns the device and inode of the pinned regular file.
func (s *Source) Identity() (dev, ino uint64) {
	if s == nil {
		return 0, 0
	}
	return s.dev, s.ino
}

// VerifyUnchanged reports ErrStaleSource if the pinned inode changed since
// Open. ctime is included so size- and mtime-preserving rewrites are detected.
func (s *Source) VerifyUnchanged() error {
	return s.withFD(func(fd int) error {
		var current unix.Stat_t
		if err := unix.Fstat(fd, &current); err != nil {
			return fmt.Errorf("fstat pinned source %q: %w", s.path, err)
		}
		mtime, ctime := statTimes(&current)
		if current.Size != s.size || !sameTimespec(mtime, s.mtime) || !sameTimespec(ctime, s.ctime) || uint64(current.Nlink) != s.nlink {
			return stale("pinned source %q changed", s.path)
		}
		return nil
	})
}

// VerifyPathStillCurrent reports ErrStaleSource if path no longer resolves to
// the file that Open pinned. It is a liveness check; the descriptor remains
// the authority for the processed bytes.
func (s *Source) VerifyPathStillCurrent() error {
	if s == nil {
		return errors.New("source is nil")
	}

	var listed unix.Stat_t
	if err := unix.Lstat(s.path, &listed); err != nil {
		return pathLstatError(s.path, err)
	}

	if s.viaSymlink {
		if listed.Mode&unix.S_IFMT != unix.S_IFLNK || uint64(listed.Dev) != s.linkDev || uint64(listed.Ino) != s.linkIno {
			return stale("source link %q changed", s.path)
		}
		var resolved unix.Stat_t
		if err := unix.Stat(s.path, &resolved); err != nil {
			if isMissing(err) {
				return stale("resolve source link %q: %v", s.path, err)
			}
			return fmt.Errorf("resolve source link %q: %w", s.path, err)
		}
		if uint64(resolved.Dev) != s.dev || uint64(resolved.Ino) != s.ino {
			return stale("source link %q retargeted", s.path)
		}
		return nil
	}

	listedMtime, listedCtime := statTimes(&listed)
	if uint64(listed.Dev) != s.dev || uint64(listed.Ino) != s.ino || listed.Size != s.size || !sameMicro(timeFromTimespec(s.mtime), listedMtime) || !sameTimespec(listedCtime, s.ctime) {
		return stale("source path %q changed", s.path)
	}
	return nil
}

// Close releases the owned descriptor. It is safe to call multiple times.
func (s *Source) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return nil
	}
	s.fd = -1
	return s.file.Close()
}

func (s *Source) withFD(fn func(int) error) error {
	if s == nil {
		return errors.New("source is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.fd < 0 {
		return errors.New("source is closed")
	}
	return fn(s.fd)
}

func verifyExpected(expected Expected, size int64, ino uint64, mtime, ctime unix.Timespec) error {
	if expected.Size != nil && *expected.Size != size {
		return stale("size %d != snapshot %d", size, *expected.Size)
	}
	if expected.ModifiedAt != nil && !sameMicro(*expected.ModifiedAt, mtime) {
		return stale("modified time does not match snapshot")
	}
	if expected.Inode != nil && uint64(*expected.Inode) != ino {
		return stale("inode does not match snapshot")
	}
	if expected.ChangedAt != nil && !sameMicro(*expected.ChangedAt, ctime) {
		return stale("changed time does not match snapshot")
	}
	return nil
}

func sameMicro(expected time.Time, actual unix.Timespec) bool {
	return expected.UTC().Truncate(time.Microsecond).Equal(timeFromTimespec(actual).UTC().Truncate(time.Microsecond))
}

func timeFromTimespec(ts unix.Timespec) time.Time {
	return time.Unix(ts.Sec, ts.Nsec)
}

func sameTimespec(a, b unix.Timespec) bool {
	return a.Sec == b.Sec && a.Nsec == b.Nsec
}

func stale(format string, arguments ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrStaleSource}, arguments...)...)
}

func isMissing(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR)
}

func pathLstatError(path string, err error) error {
	if isMissing(err) {
		return stale("lstat source %q: %v", path, err)
	}
	return fmt.Errorf("lstat source %q: %w", path, err)
}

func closeOpenFD(fd int, result error) error {
	if err := unix.Close(fd); err != nil {
		return errors.Join(result, err)
	}
	return result
}

func closeOpenFile(file *os.File, result error) error {
	if err := file.Close(); err != nil {
		return errors.Join(result, err)
	}
	return result
}
