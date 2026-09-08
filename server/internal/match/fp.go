package match

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// fp is a small deterministic hash builder used by Fingerprint.
type fp struct{ h hash.Hash }

func newHash() *fp { return &fp{h: sha256.New()} }

func (f *fp) addByte(b byte)        { _, _ = f.h.Write([]byte{b}) }
func (f *fp) addU64(v uint64)       { var b [8]byte; binary.BigEndian.PutUint64(b[:], v); _, _ = f.h.Write(b[:]) }
func (f *fp) addI64(v int64)        { f.addU64(uint64(v)) }
func (f *fp) addString(s string)    { f.addU64(uint64(len(s))); _, _ = f.h.Write([]byte(s)) }
func (f *fp) hex() string           { return hex.EncodeToString(f.h.Sum(nil)) }
