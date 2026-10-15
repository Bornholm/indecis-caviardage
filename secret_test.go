//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func newTestSecret(t *testing.T) *secret {
	t.Helper()
	if _, err := checkSecret(); err != nil {
		t.Skip("no secret memory:", err)
	}
	s := &secret{}
	t.Cleanup(s.Close)
	return s
}

// What is written comes back whole, across several segments.
func TestSecretRoundTrip(t *testing.T) {
	s := newTestSecret(t)
	data := bytes.Repeat([]byte("Jean Dupont;06 12 34 56 78\n"), 20_000) // 540 KB: 4 segments
	if _, err := io.Copy(s, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(s.Reader())
	if err != nil || !bytes.Equal(got, data) || s.Size() != int64(len(data)) || len(s.segs) < 2 {
		t.Fatalf("read back %d bytes of %d, %d segments, %v", len(got), len(data), len(s.segs), err)
	}
	s.Close()
	if got, _ := io.ReadAll(s.Reader()); len(got) != 0 || s.Size() != 0 {
		t.Error("a closed secret still holds data")
	}
}

// The process itself cannot read a secret page through /proc/self/mem,
// as root or a debugger could not: the page is out of the kernel's
// mapping. An ordinary page reads.
func TestSecretUnreadable(t *testing.T) {
	s := newTestSecret(t)
	s.Write([]byte("SECRET-MARKER"))
	plain := []byte("PLAIN--MARKER")
	mem, err := os.Open("/proc/self/mem")
	if err != nil {
		t.Skip(err)
	}
	defer mem.Close()
	buf := make([]byte, 13)
	if _, err := mem.ReadAt(buf, int64(uintptr(unsafe.Pointer(&plain[0])))); err != nil || string(buf) != "PLAIN--MARKER" {
		t.Fatalf("an ordinary page should read: %q, %v", buf, err)
	}
	if _, err := mem.ReadAt(buf, int64(uintptr(unsafe.Pointer(&s.segs[0][0])))); err == nil {
		t.Errorf("a secret page read through /proc/self/mem: %q", buf)
	}
}

// Beyond the locked-memory limit, a write fails with errSecretFull.
func TestSecretFull(t *testing.T) {
	s := newTestSecret(t)
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rl); err != nil || rl.Cur > 64<<20 {
		t.Skip("locked-memory limit too high to reach in a test")
	}
	chunk := make([]byte, 1<<20)
	var err error
	for written := uint64(0); written <= rl.Cur && err == nil; written += uint64(len(chunk)) {
		_, err = s.Write(chunk)
	}
	if !errors.Is(err, errSecretFull) {
		t.Errorf("err = %v, want errSecretFull", err)
	}
}
