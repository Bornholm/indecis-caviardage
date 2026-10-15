//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

// A secret holds a document in secret memory (memfd_secret, Linux 5.14):
// pages removed from the kernel's mapping, that no other process can
// read, root included, through /proc/<pid>/mem or ptrace, that never go
// to swap and stay out of core dumps. The documents waiting for a model
// and for their download stay there.
//
// The memory counts against the locked-memory limit (RLIMIT_MEMLOCK, 8
// MB by default): a secret that would exceed it fails with
// errSecretFull. A secret grows by segments, each twice the previous one,
// from 64 KB to 16 MB: its final size need not be known.
type secret struct {
	segs [][]byte
	n    int // bytes written to the last segment
	size int64
}

var errSecretFull = errors.New("mémoire protégée insuffisante")

const (
	secretFirst = 64 << 10
	secretMax   = 16 << 20
)

// checkSecret tells whether secret memory is available, and the
// locked-memory limit it counts against.
func checkSecret() (limit uint64, err error) {
	var s secret
	if _, err := s.Write([]byte{0}); err != nil {
		return 0, err
	}
	s.Close()
	var rl unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rl); err != nil {
		return 0, err
	}
	return rl.Cur, nil
}

// protectProcess forbids core dumps of the process and its reading by
// another process of the same user (ptrace, /proc/<pid>/mem): while it is
// processed, a document is also in Go's memory, which cannot be secret.
func protectProcess() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}

func (s *secret) grow() error {
	size := secretFirst
	if len(s.segs) > 0 {
		size = min(2*len(s.segs[len(s.segs)-1]), secretMax)
	}
	fd, err := unix.MemfdSecret(0)
	if err != nil {
		return fmt.Errorf("memfd_secret : %w", err)
	}
	defer unix.Close(fd) // the mapping keeps the memory
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		return fmt.Errorf("memfd_secret : %w", err)
	}
	b, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.ENOMEM) {
		return errSecretFull
	}
	if err != nil {
		return fmt.Errorf("memfd_secret : %w", err)
	}
	s.segs = append(s.segs, b)
	s.n = 0
	return nil
}

// Write implements io.Writer.
func (s *secret) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		if len(s.segs) == 0 || s.n == len(s.segs[len(s.segs)-1]) {
			if err := s.grow(); err != nil {
				return written, err
			}
		}
		c := copy(s.segs[len(s.segs)-1][s.n:], p)
		s.n += c
		s.size += int64(c)
		written += c
		p = p[c:]
	}
	return written, nil
}

// Size returns the number of bytes written.
func (s *secret) Size() int64 { return s.size }

// Reader reads what was written, from the start.
func (s *secret) Reader() io.Reader {
	readers := make([]io.Reader, len(s.segs))
	for i, b := range s.segs {
		if i == len(s.segs)-1 {
			b = b[:s.n]
		}
		readers[i] = &secretReader{b: b}
	}
	return io.MultiReader(readers...)
}

// secretReader reads a segment without copying it elsewhere than to the
// caller's buffer.
type secretReader struct {
	b   []byte
	off int
}

func (r *secretReader) Read(p []byte) (int, error) {
	if r.off == len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

// Close overwrites the memory with zeros and releases it.
func (s *secret) Close() {
	for _, b := range s.segs {
		clear(b)
		unix.Munmap(b)
	}
	s.segs, s.n, s.size = nil, 0, 0
}
