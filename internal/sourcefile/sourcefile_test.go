// SPDX-License-Identifier: GPL-2.0-only

package sourcefile_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deadfin-org/deadfin/internal/clock"
	"github.com/deadfin-org/deadfin/internal/sourcefile"
	"golang.org/x/sys/unix"
)

func TestOpenPinsRegularFileAgainstSnapshotFacts(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "pinned media")
	expected := expectedForStat(t, path)

	source := openSource(t, path, expected)
	if source.File() == nil {
		t.Fatal("File() = nil, want borrowed source descriptor")
	}
	if got, want := readAt(t, source.File()), "pinned media"; got != want {
		t.Errorf("File() contents = %q, want %q", got, want)
	}
	if dev, ino := source.Identity(); dev == 0 || ino == 0 {
		t.Errorf("Identity() = (%d, %d), want non-zero device and inode", dev, ino)
	}
	if err := source.VerifyUnchanged(); err != nil {
		t.Fatalf("VerifyUnchanged(): %v", err)
	}
	if err := source.VerifyPathStillCurrent(); err != nil {
		t.Fatalf("VerifyPathStillCurrent(): %v", err)
	}
}

func TestOpenAcceptsUnknownAndMicrosecondEquivalentSnapshotFacts(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "pinned media")

	t.Run("unknown facts", func(t *testing.T) {
		source := openSource(t, path, sourcefile.Expected{})
		if err := source.VerifyUnchanged(); err != nil {
			t.Fatalf("VerifyUnchanged(): %v", err)
		}
	})

	t.Run("nanoseconds within snapshot microsecond", func(t *testing.T) {
		expected := expectedForStat(t, path)
		micro := expected.ModifiedAt.UTC().Truncate(time.Microsecond)
		withinSameMicrosecond := micro.Add(999 * time.Nanosecond)
		expected.ModifiedAt = &withinSameMicrosecond

		source := openSource(t, path, expected)
		if err := source.VerifyUnchanged(); err != nil {
			t.Fatalf("VerifyUnchanged(): %v", err)
		}
	})
}

func TestOpenValidatesExpectedInodeAndChangedAt(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "pinned media")

	t.Run("accepts matching inode and changed time within the same microsecond", func(t *testing.T) {
		expected := expectedIdentityForStat(t, path)
		changedAt := expected.ChangedAt.UTC().Truncate(time.Microsecond).Add(999 * time.Nanosecond)
		expected.ChangedAt = &changedAt

		source := openSource(t, path, expected)
		if err := source.VerifyUnchanged(); err != nil {
			t.Fatalf("VerifyUnchanged(): %v", err)
		}
	})

	t.Run("rejects a different inode", func(t *testing.T) {
		expected := expectedIdentityForStat(t, path)
		wrongInode := *expected.Inode + 1
		expected.Inode = &wrongInode

		assertOpenStale(t, path, expected)
	})

	t.Run("rejects a changed time outside the snapshot microsecond", func(t *testing.T) {
		expected := expectedIdentityForStat(t, path)
		wrongChangedAt := expected.ChangedAt.Add(time.Microsecond)
		expected.ChangedAt = &wrongChangedAt

		assertOpenStale(t, path, expected)
	})
}

func TestOpenRejectsStaleOrNonRegularSources(t *testing.T) {
	t.Run("size mismatch", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "abc")
		expected := expectedForStat(t, path)
		wrongSize := *expected.Size + 1
		expected.Size = &wrongSize
		assertOpenStale(t, path, expected)
	})

	t.Run("modified time mismatch", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "abc")
		expected := expectedForStat(t, path)
		wrongModified := expected.ModifiedAt.Add(time.Microsecond)
		expected.ModifiedAt = &wrongModified
		assertOpenStale(t, path, expected)
	})

	t.Run("missing path", func(t *testing.T) {
		assertOpenStale(t, filepath.Join(t.TempDir(), "missing.mkv"), sourcefile.Expected{})
	})

	t.Run("directory", func(t *testing.T) {
		assertOpenStale(t, t.TempDir(), sourcefile.Expected{})
	})

	t.Run("fifo returns promptly", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.fifo")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("Mkfifo(%q): %v", path, err)
		}

		result := make(chan error, 1)
		go func() {
			source, err := sourcefile.Open(path, sourcefile.Expected{})
			if source != nil {
				_ = source.Close()
			}
			result <- err
		}()
		select {
		case err := <-result:
			if !errors.Is(err, sourcefile.ErrStaleSource) {
				t.Errorf("Open(FIFO) error = %v, want ErrStaleSource", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Open(FIFO) blocked; source opens must use non-blocking mode before rejecting non-regular files")
		}
	})
}

func TestVerificationRejectsRenameReplacement(t *testing.T) {
	base := t.TempDir()
	path := writeSourceFileAt(t, filepath.Join(base, "movie.mkv"), "original")
	source := openSource(t, path, expectedForStat(t, path))
	settleTimestamps(t, base)

	replacement := writeSourceFileAt(t, filepath.Join(base, "replacement.mkv"), "replacement")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("Rename(%q, %q): %v", replacement, path, err)
	}

	assertStale(t, "VerifyUnchanged() after pathname replacement", source.VerifyUnchanged())
	assertStale(t, "VerifyPathStillCurrent() after pathname replacement", source.VerifyPathStillCurrent())
	if got, want := readAt(t, source.File()), "original"; got != want {
		t.Errorf("borrowed descriptor after pathname replacement = %q, want original bytes %q", got, want)
	}
}

func TestVerifyUnchangedRejectsRepeatedOpenInodeMutations(t *testing.T) {
	for range 8 {
		t.Run("append", func(t *testing.T) {
			path := writeSourceFile(t, "movie.mkv", "original")
			source := openSource(t, path, expectedForStat(t, path))
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatalf("OpenFile(%q): %v", path, err)
			}
			if _, err := file.WriteString("+"); err != nil {
				_ = file.Close()
				t.Fatalf("Append(%q): %v", path, err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("Close(%q): %v", path, err)
			}
			assertStale(t, "VerifyUnchanged() after append", source.VerifyUnchanged())
		})

		t.Run("same size rewrite with restored mtime", func(t *testing.T) {
			path := writeSourceFile(t, "movie.mkv", "original")
			source := openSource(t, path, expectedForStat(t, path))
			before, err := source.File().Stat()
			if err != nil {
				t.Fatalf("File().Stat(): %v", err)
			}
			settleTimestamps(t, filepath.Dir(path))
			if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
				t.Fatalf("WriteFile(%q): %v", path, err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatalf("Chtimes(%q): %v", path, err)
			}
			assertStale(t, "VerifyUnchanged() after size-preserving rewrite with restored mtime", source.VerifyUnchanged())
		})
	}
}

func TestVerificationRejectsSameSizeRewriteAfterSiblingHardlinkUnlink(t *testing.T) {
	base := t.TempDir()
	path := writeSourceFileAt(t, filepath.Join(base, "movie.mkv"), "original")
	sibling := filepath.Join(base, "movie-link.mkv")
	if err := os.Link(path, sibling); err != nil {
		t.Fatalf("Link(%q, %q): %v", path, sibling, err)
	}

	source := openSource(t, path, expectedForStat(t, path))
	pinnedDev, pinnedIno := source.Identity()
	if pinnedDev == 0 || pinnedIno == 0 {
		t.Skip("platform does not expose stable device and inode identity for hardlinks")
	}

	var before unix.Stat_t
	if err := unix.Fstat(int(source.File().Fd()), &before); err != nil {
		t.Fatalf("Fstat(pinned source): %v", err)
	}
	if uint64(before.Dev) != pinnedDev || uint64(before.Ino) != pinnedIno || before.Nlink != 2 {
		t.Fatalf("pinned source identity = (%d, %d, links=%d), want (%d, %d, links=2)", before.Dev, before.Ino, before.Nlink, pinnedDev, pinnedIno)
	}

	settleTimestamps(t, base)
	if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if err := os.Chtimes(path, time.Unix(before.Mtim.Sec, before.Mtim.Nsec), time.Unix(before.Mtim.Sec, before.Mtim.Nsec)); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
	if err := os.Remove(sibling); err != nil {
		t.Fatalf("Remove(%q): %v", sibling, err)
	}

	var after unix.Stat_t
	if err := unix.Fstat(int(source.File().Fd()), &after); err != nil {
		t.Fatalf("Fstat(pinned source) after rewrite: %v", err)
	}
	if after.Size != before.Size || after.Mtim != before.Mtim || after.Nlink != 1 {
		t.Fatalf("rewrite fixture facts = (size=%d, mtime=%v, links=%d), want (size=%d, mtime=%v, links=1)", after.Size, after.Mtim, after.Nlink, before.Size, before.Mtim)
	}
	if after.Ctim == before.Ctim {
		t.Fatal("rewrite fixture did not change ctime")
	}
	if got, want := readAt(t, source.File()), "replaced"; got != want {
		t.Fatalf("pinned descriptor after rewrite = %q, want changed bytes %q", got, want)
	}

	assertStale(t, "VerifyUnchanged() after same-size rewrite and sibling unlink", source.VerifyUnchanged())
	assertStale(t, "VerifyPathStillCurrent() after same-size rewrite and sibling unlink", source.VerifyPathStillCurrent())
}

func TestSymlinkSourceDetectsRetargetAndTargetMutation(t *testing.T) {
	t.Run("retarget", func(t *testing.T) {
		base := t.TempDir()
		first := writeSourceFileAt(t, filepath.Join(base, "first.mkv"), "first target")
		second := writeSourceFileAt(t, filepath.Join(base, "second.mkv"), "second target")
		link := filepath.Join(base, "movie.mkv")
		if err := os.Symlink(first, link); err != nil {
			t.Fatalf("Symlink(%q, %q): %v", first, link, err)
		}
		source := openSource(t, link, expectedIdentityForStat(t, link))

		if err := os.Remove(link); err != nil {
			t.Fatalf("Remove(%q): %v", link, err)
		}
		if err := os.Symlink(second, link); err != nil {
			t.Fatalf("Symlink(%q, %q): %v", second, link, err)
		}
		assertStale(t, "VerifyPathStillCurrent() after symlink retarget", source.VerifyPathStillCurrent())
		if got, want := readAt(t, source.File()), "first target"; got != want {
			t.Errorf("borrowed descriptor after symlink retarget = %q, want first target %q", got, want)
		}
	})

	t.Run("target mutation", func(t *testing.T) {
		base := t.TempDir()
		target := writeSourceFileAt(t, filepath.Join(base, "target.mkv"), "target")
		link := filepath.Join(base, "movie.mkv")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(%q, %q): %v", target, link, err)
		}
		source := openSource(t, link, expectedIdentityForStat(t, link))
		file, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("OpenFile(%q): %v", target, err)
		}
		if _, err := file.WriteString("+"); err != nil {
			_ = file.Close()
			t.Fatalf("Append(%q): %v", target, err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close(%q): %v", target, err)
		}
		assertStale(t, "VerifyUnchanged() after symlink target mutation", source.VerifyUnchanged())
	})
}

// TestSymlinkSourceVerifiesTargetFacts proves that a link snapshot carries
// the facts of the link target, as the library scanner records them. The
// link's own lstat facts do not describe the pinned file and are stale.
func TestSymlinkSourceVerifiesTargetFacts(t *testing.T) {
	base := t.TempDir()
	target := writeSourceFileAt(t, filepath.Join(base, "target.mkv"), "target bytes of a different length")
	link := filepath.Join(base, "movie.mkv")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", target, link, err)
	}
	source := openSource(t, link, expectedIdentityForStat(t, link))
	if err := source.VerifyPathStillCurrent(); err != nil {
		t.Errorf("VerifyPathStillCurrent() = %v, want nil", err)
	}
	assertOpenStale(t, link, expectedForLstat(t, link))
}

func TestSourceAccessorsRetainOpenTimeFacts(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "original")
	source := openSource(t, path, expectedForStat(t, path))
	opened := source.Fingerprint()
	if got, want := opened, fingerprintForFile(t, source.File()); got != want {
		t.Fatalf("Fingerprint() = %+v, want open-time descriptor facts %+v", got, want)
	}

	if got, want := source.Path(), path; got != want {
		t.Errorf("Path() = %q, want opening path %q", got, want)
	}
	if got, want := readAt(t, source.File()), "original"; got != want {
		t.Fatalf("File() contents = %q, want opened bytes %q", got, want)
	}

	settleTimestamps(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if got := source.Fingerprint(); got != opened {
		t.Errorf("Fingerprint() after source mutation = %+v, want immutable open-time facts %+v", got, opened)
	}
	assertStale(t, "VerifyUnchanged() after source mutation", source.VerifyUnchanged())
}

func TestSourceFileIsBorrowedAndSourceOwnsClose(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "pinned media")
	source, err := sourcefile.Open(path, expectedForStat(t, path))
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	file := source.File()
	fd := int(file.Fd())
	if got, want := readAt(t, file), "pinned media"; got != want {
		t.Errorf("File() contents before Close = %q, want %q", got, want)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("first Close(): %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); !errors.Is(err, unix.EBADF) {
		t.Errorf("Fstat(borrowed fd) after Close error = %v, want EBADF", err)
	}
}

func TestOpenAuxiliaryPinsIndependentVerifiedDescriptor(t *testing.T) {
	t.Run("independent open file description shares source identity", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "pinned media")
		source := openSource(t, path, expectedForStat(t, path))

		auxiliary, err := source.OpenAuxiliary()
		if err != nil {
			t.Fatalf("OpenAuxiliary(): %v", err)
		}
		t.Cleanup(func() {
			if err := auxiliary.Close(); err != nil {
				t.Errorf("auxiliary Close(): %v", err)
			}
		})

		var auxiliaryStat unix.Stat_t
		if err := unix.Fstat(int(auxiliary.Fd()), &auxiliaryStat); err != nil {
			t.Fatalf("Fstat(auxiliary): %v", err)
		}
		if dev, ino := source.Identity(); uint64(auxiliaryStat.Dev) != dev || uint64(auxiliaryStat.Ino) != ino {
			t.Fatalf("auxiliary identity = (%d, %d), want primary identity (%d, %d)", auxiliaryStat.Dev, auxiliaryStat.Ino, dev, ino)
		}

		if got, want := readSequentially(t, auxiliary), "pinned media"; got != want {
			t.Fatalf("auxiliary contents = %q, want %q", got, want)
		}
		if got, want := readSequentially(t, source.File()), "pinned media"; got != want {
			t.Fatalf("primary contents after auxiliary read = %q, want %q; descriptors share an open-file description", got, want)
		}
	})

	t.Run("closing auxiliary leaves primary usable", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "pinned media")
		source := openSource(t, path, expectedForStat(t, path))
		auxiliary, err := source.OpenAuxiliary()
		if err != nil {
			t.Fatalf("OpenAuxiliary(): %v", err)
		}
		if err := auxiliary.Close(); err != nil {
			t.Fatalf("auxiliary Close(): %v", err)
		}
		if err := source.VerifyUnchanged(); err != nil {
			t.Fatalf("VerifyUnchanged() after auxiliary Close(): %v", err)
		}
		if got, want := readSequentially(t, source.File()), "pinned media"; got != want {
			t.Fatalf("primary contents after auxiliary Close() = %q, want %q", got, want)
		}
	})

	t.Run("replacement is stale", func(t *testing.T) {
		base := t.TempDir()
		path := writeSourceFileAt(t, filepath.Join(base, "movie.mkv"), "original")
		source := openSource(t, path, expectedForStat(t, path))
		replacement := writeSourceFileAt(t, filepath.Join(base, "replacement.mkv"), "replacement")
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("Rename(%q, %q): %v", replacement, path, err)
		}
		auxiliary, err := source.OpenAuxiliary()
		if auxiliary != nil {
			_ = auxiliary.Close()
			t.Fatal("OpenAuxiliary() returned a descriptor for replacement")
		}
		assertStale(t, "OpenAuxiliary() after replacement", err)
	})

	t.Run("same-size rewrite with restored mtime is stale", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "original")
		source := openSource(t, path, expectedForStat(t, path))
		before, err := source.File().Stat()
		if err != nil {
			t.Fatalf("primary Stat(): %v", err)
		}
		settleTimestamps(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte("replaced"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
		if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
			t.Fatalf("Chtimes(%q): %v", path, err)
		}
		auxiliary, err := source.OpenAuxiliary()
		if auxiliary != nil {
			_ = auxiliary.Close()
			t.Fatal("OpenAuxiliary() returned a descriptor for rewritten source")
		}
		assertStale(t, "OpenAuxiliary() after same-size rewrite", err)
	})

	t.Run("closed source returns an error", func(t *testing.T) {
		path := writeSourceFile(t, "movie.mkv", "pinned media")
		source := openSource(t, path, expectedForStat(t, path))
		if err := source.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
		auxiliary, err := source.OpenAuxiliary()
		if auxiliary != nil {
			_ = auxiliary.Close()
			t.Fatal("OpenAuxiliary() returned a descriptor after source Close()")
		}
		if err == nil {
			t.Fatal("OpenAuxiliary() after source Close() error = nil")
		}
	})
}

func TestRewindRestartsBorrowedDescriptorAndRejectsClosedSource(t *testing.T) {
	path := writeSourceFile(t, "movie.mkv", "pinned media")
	source := openSource(t, path, expectedForStat(t, path))

	if got, want := readSequentially(t, source.File()), "pinned media"; got != want {
		t.Fatalf("initial sequential read = %q, want %q", got, want)
	}
	if err := source.Rewind(); err != nil {
		t.Fatalf("Rewind(): %v", err)
	}
	if got, want := readSequentially(t, source.File()), "pinned media"; got != want {
		t.Fatalf("sequential read after Rewind() = %q, want %q", got, want)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := source.Rewind(); err == nil {
		t.Fatal("Rewind() after Close() error = nil")
	}
}

func TestFDPathAddressesInheritedDescriptor(t *testing.T) {
	if got, want := sourcefile.FDPath(7), "/dev/fd/7"; got != want {
		t.Errorf("FDPath(7) = %q, want %q", got, want)
	}
}

// settleTimestamps blocks until this filesystem records a ctime distinct from
// anything stamped before the call, then returns.
//
// The staleness tests below deliberately neutralise every signal except ctime:
// a rename replacement leaves the pinned inode's size and mtime untouched, and
// a same-size rewrite with a restored mtime leaves only the ctime bump from the
// write itself. Each mutation completes in microseconds, so on a volume whose
// timestamps are coarser than that the mutation lands inside the same tick the
// source was pinned in and becomes genuinely invisible to Fstat. That is a
// property of the volume, not of the code under test: these tests passed on a
// nanosecond-stamped local filesystem and failed on the CI runner's container
// filesystem, with individual iterations of one loop passing and failing at
// random depending on where they fell relative to a tick boundary.
//
// Calling this between pinning a source and mutating it guarantees the mutation
// carries a distinguishable ctime, so the assertion keeps its full meaning
// instead of depending on the volume's resolution. It self-calibrates rather
// than sleeping a fixed amount, and fails loudly if the filesystem cannot
// distinguish two consecutive inode changes at all.
func settleTimestamps(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, ".timestamp-tick")
	if err := os.WriteFile(probe, []byte("0"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", probe, err)
	}
	defer func() { _ = os.Remove(probe) }()

	var first unix.Stat_t
	if err := unix.Stat(probe, &first); err != nil {
		t.Fatalf("Stat(%q): %v", probe, err)
	}
	deadline := clock.NewReal().Now().Add(2 * time.Second)
	for {
		if err := os.WriteFile(probe, []byte("1"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", probe, err)
		}
		var next unix.Stat_t
		if err := unix.Stat(probe, &next); err != nil {
			t.Fatalf("Stat(%q): %v", probe, err)
		}
		if next.Ctim != first.Ctim {
			return
		}
		if clock.NewReal().Now().After(deadline) {
			t.Fatalf("filesystem at %q records no ctime change between two consecutive writes within 2s; staleness detection cannot be asserted on this volume", dir)
		}
		time.Sleep(time.Millisecond)
	}
}

func openSource(t *testing.T, path string, expected sourcefile.Expected) *sourcefile.Source {
	t.Helper()
	source, err := sourcefile.Open(path, expected)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("Close(%q): %v", path, err)
		}
	})
	return source
}

func assertOpenStale(t *testing.T, path string, expected sourcefile.Expected) {
	t.Helper()
	source, err := sourcefile.Open(path, expected)
	if source != nil {
		_ = source.Close()
		t.Errorf("Open(%q) returned a source with error %v", path, err)
	}
	assertStale(t, "Open("+path+")", err)
}

func assertStale(t *testing.T, operation string, err error) {
	t.Helper()
	if !errors.Is(err, sourcefile.ErrStaleSource) {
		t.Errorf("%s error = %v, want ErrStaleSource", operation, err)
	}
}

func expectedForStat(t *testing.T, path string) sourcefile.Expected {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	return expectedFromInfo(info)
}

func expectedForLstat(t *testing.T, path string) sourcefile.Expected {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%q): %v", path, err)
	}
	return expectedFromInfo(info)
}

func expectedFromInfo(info os.FileInfo) sourcefile.Expected {
	size := info.Size()
	modified := info.ModTime()
	return sourcefile.Expected{Size: &size, ModifiedAt: &modified}
}

func expectedIdentityForStat(t *testing.T, path string) sourcefile.Expected {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}

	size := stat.Size
	modifiedAt := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
	inode := int64(stat.Ino)
	changedAt := time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
	return sourcefile.Expected{
		Size:       &size,
		ModifiedAt: &modifiedAt,
		Inode:      &inode,
		ChangedAt:  &changedAt,
	}
}

func fingerprintForFile(t *testing.T, file *os.File) sourcefile.Fingerprint {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatalf("Fstat(source file): %v", err)
	}
	return sourcefile.Fingerprint{
		Dev:   uint64(stat.Dev),
		Ino:   uint64(stat.Ino),
		Size:  stat.Size,
		MTime: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec),
		CTime: time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec),
	}
}

func writeSourceFile(t *testing.T, name, contents string) string {
	t.Helper()
	return writeSourceFileAt(t, filepath.Join(t.TempDir(), name), contents)
}

func writeSourceFileAt(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

func readAt(t *testing.T, file *os.File) string {
	t.Helper()
	data := make([]byte, 128)
	n, err := file.ReadAt(data, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt(): %v", err)
	}
	return string(data[:n])
}

func readSequentially(t *testing.T, file *os.File) string {
	t.Helper()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll(): %v", err)
	}
	return string(data)
}
