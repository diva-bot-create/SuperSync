package audio

import (
	"encoding/binary"
	"math"
)

// extendedToFloat decodes an 80-bit IEEE 754 extended float (used by AIFF COMM).
func extendedToFloat(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:2]))
	mant := binary.BigEndian.Uint64(b[2:10])
	sign := 1.0
	if exp&0x8000 != 0 {
		sign = -1
		exp &= 0x7fff
	}
	if exp == 0 && mant == 0 {
		return 0
	}
	return sign * float64(mant) * math.Pow(2, float64(exp-16383-63))
}
