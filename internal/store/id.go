package store

import (
	"crypto/rand"
	"encoding/binary"
)

func randUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return binary.BigEndian.Uint32(b[:])
}
