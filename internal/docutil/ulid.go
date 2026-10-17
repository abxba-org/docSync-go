package docutil

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// Crockford base32 alphabet (no I, L, O, U) — the canonical ULID encoding.
const ulidEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	ulidTimeChars   = 10
	ulidRandomChars = 16
	ulidRandomBytes = 10
	ulidTimeBytes   = 6
)

// ULIDGenerator generates canonical 26-char Crockford-base32 ULIDs
// (10-char 48-bit millisecond timestamp + 16-char 80-bit randomness), ported
// from core/src/util/ulid.ts.
//
// Monotonicity guarantee: successive calls never produce a lexicographically
// smaller value. Within the same millisecond (or when the observed clock
// steps backwards) the timestamp is held and the 80-bit randomness is
// incremented instead — so bulk-generated ids sort stably. Overflow of the
// randomness space (2^80 increments within one millisecond) wraps via
// masking and is practically unreachable.
type ULIDGenerator struct {
	mu         sync.Mutex
	lastTime   int64 // -1 = fresh
	lastRandom [ulidRandomBytes]byte
	nowMs      func() int64
	randBytes  func(n int) ([]byte, error)
}

// NewULIDGenerator returns a generator reading the wall clock and crypto/rand.
func NewULIDGenerator() *ULIDGenerator {
	return &ULIDGenerator{
		lastTime: -1,
		nowMs:    func() int64 { return time.Now().UnixMilli() },
		randBytes: func(n int) ([]byte, error) {
			buf := make([]byte, n)
			if _, err := rand.Read(buf); err != nil {
				return nil, err
			}
			return buf, nil
		},
	}
}

// NewULIDGeneratorWith returns a deterministic generator for tests: nowMs
// supplies the clock, rng supplies the random bytes.
func NewULIDGeneratorWith(nowMs func() int64, rng func(n int) ([]byte, error)) *ULIDGenerator {
	return &ULIDGenerator{
		lastTime:  -1,
		nowMs:     nowMs,
		randBytes: rng,
	}
}

// Reset restores a fresh sequence (used by tests and process-fresh tooling).
func (g *ULIDGenerator) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastTime = -1
	g.lastRandom = [ulidRandomBytes]byte{}
}

// Generate returns a canonical 26-char ULID.
func (g *ULIDGenerator) Generate() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	requested := g.nowMs()
	const timeMax = int64(1)<<48 - 1
	if requested < 0 || requested > timeMax {
		return "", &Error{Code: CodeInternal, Msg: fmt.Sprintf("ulid: timestamp out of the 48-bit range: %d", requested)}
	}
	fresh, err := g.randBytes(ulidRandomBytes)
	if err != nil {
		return "", err
	}
	var freshRandom [ulidRandomBytes]byte
	copy(freshRandom[:], fresh)

	var timeMs int64
	var randomness [ulidRandomBytes]byte
	if requested <= g.lastTime && g.lastTime >= 0 {
		timeMs = g.lastTime
		randomness = incrementRandomness(g.lastRandom)
	} else {
		timeMs = requested
		randomness = freshRandom
	}
	g.lastTime = timeMs
	g.lastRandom = randomness
	out := make([]byte, 0, ulidTimeChars+ulidRandomChars)
	out = append(out, encodeBase32(timeMs, ulidTimeBytes, ulidTimeChars)...)
	out = append(out, encodeBytesBase32(randomness[:], ulidRandomChars)...)
	return string(out), nil
}

func incrementRandomness(b [ulidRandomBytes]byte) [ulidRandomBytes]byte {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return b
		}
	}
	return b // wrapped at 2^80
}

// encodeBase32 encodes the big-endian value in srcBytes bytes into n chars.
func encodeBase32(value int64, srcBytes, n int) []byte {
	var buf [8]byte
	for i := srcBytes - 1; i >= 0; i-- {
		buf[i] = byte(value)
		value >>= 8
	}
	return encodeBytesBase32(buf[:srcBytes], n)
}

// encodeBytesBase32 treats src as a big-endian unsigned integer and repeatedly
// divides it by 32, emitting remainder chars from least to most significant.
func encodeBytesBase32(src []byte, n int) []byte {
	tmp := make([]byte, len(src))
	copy(tmp, src)
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		var rem uint16
		for j := 0; j < len(tmp); j++ {
			cur := rem<<8 | uint16(tmp[j])
			tmp[j] = byte(cur / 32)
			rem = cur % 32
		}
		out[i] = ulidEncoding[rem]
	}
	return out
}

// IsValidULID reports whether value is a canonical 26-char Crockford-base32 ULID.
func IsValidULID(value string) bool {
	if len(value) != 26 {
		return false
	}
	for i := 0; i < 26; i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z' && c != 'I' && c != 'L' && c != 'O' && c != 'U':
		default:
			return false
		}
	}
	return true
}
