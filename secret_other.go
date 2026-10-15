//go:build !linux && !js

package main

import (
	"errors"
	"io"
)

// Secret memory (memfd_secret) only exists on Linux: elsewhere, the
// server refuses to start rather than keep documents in readable memory.
type secret struct{}

var errSecretFull = errors.New("mémoire protégée insuffisante")

var errNoSecret = errors.New("mémoire protégée (memfd_secret) disponible sous Linux seulement")

func checkSecret() (uint64, error)          { return 0, errNoSecret }
func protectProcess() error                 { return errNoSecret }
func (s *secret) Write([]byte) (int, error) { return 0, errNoSecret }
func (s *secret) Size() int64               { return 0 }
func (s *secret) Reader() io.Reader         { return eofReader{} }
func (s *secret) Close()                    {}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }
