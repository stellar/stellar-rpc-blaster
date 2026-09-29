package state

import (
	"encoding/binary"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// DeriveKeypair deterministically derives a keypair from base by overwriting
// the last 4 bytes of its raw seed with the big-endian representation of index.
// Callers must ensure index > 0 so the derived keypair differs from the base.
func DeriveKeypair(base *keypair.Full, index int) (*keypair.Full, error) {
	rawSeed, err := strkey.Decode(strkey.VersionByteSeed, base.Seed())
	if err != nil {
		return nil, err
	}
	var derived [32]byte
	copy(derived[:], rawSeed)
	binary.BigEndian.PutUint32(derived[28:], uint32(index))
	return keypair.FromRawSeed(derived)
}
