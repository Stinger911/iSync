package real

// This file implements the Apple/iCloud SRP-6a client protocol used by the
// current iCloud web login flow. It follows RFC 5054's 2048-bit group with
// Apple's documented-in-practice `NoUserNameInX` and password derivation
// variants. The algorithm was independently reviewed against rclone's
// MIT-licensed iclouddrive backend (backend/iclouddrive/api/srp.go,
// commit 4a00a4dc4bbf21aa6f112036c644701968c7234b).

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"

	"golang.org/x/crypto/pbkdf2"
)

var (
	srpN = mustParseBigHex(
		"AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050" +
			"A37329CBB4A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50" +
			"E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B8" +
			"55F97993EC975EEAA80D740ADBF4FF747359D041D5C33EA71D281E446B14773B" +
			"CA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748" +
			"544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB37861602790" +
			"04E57AE6AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8" +
			"E9DBFBB694B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F" +
			"9E4AFF73")
	srpG         = big.NewInt(2)
	srpNLenBytes = 256
)

func mustParseBigHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("apiclient/real: invalid SRP group parameter")
	}
	return n
}

type srpClient struct {
	a  *big.Int
	A  *big.Int
	k  *big.Int
	M1 []byte
	M2 []byte
}

func newSRPClient() (*srpClient, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("apiclient/real: SRP random secret: %w", err)
	}
	a := new(big.Int).SetBytes(secret)
	return &srpClient{a: a, A: new(big.Int).Exp(srpG, a, srpN), k: srpMultiplier()}, nil
}

func (c *srpClient) publicValue() []byte { return padToN(c.A) }

func (c *srpClient) processChallenge(username, derivedKey, salt, rawB []byte) error {
	B := new(big.Int).SetBytes(rawB)
	if B.Sign() <= 0 || B.Cmp(srpN) >= 0 {
		return fmt.Errorf("apiclient/real: invalid SRP server public value")
	}

	x := srpX(salt, derivedKey)
	u := srpU(c.A, B)
	if u.Sign() == 0 {
		return fmt.Errorf("apiclient/real: invalid zero SRP scrambling parameter")
	}

	gx := new(big.Int).Exp(srpG, x, srpN)
	base := new(big.Int).Sub(B, new(big.Int).Mul(c.k, gx))
	exponent := new(big.Int).Add(c.a, new(big.Int).Mul(u, x))
	S := new(big.Int).Exp(base, exponent, srpN)
	K := srpHash(padToN(S))

	A := padToN(c.A)
	paddedB := padToN(B)
	c.M1 = srpM1(username, salt, A, paddedB, K)
	c.M2 = srpM2(A, c.M1, K)
	return nil
}

// derivePassword implements Apple's two supported password KDF protocols.
func derivePassword(password string, salt []byte, iterations int, protocol string) ([]byte, error) {
	if iterations <= 0 {
		return nil, fmt.Errorf("apiclient/real: invalid SRP iteration count %d", iterations)
	}
	h := sha256.Sum256([]byte(password))
	var input []byte
	switch protocol {
	case "s2k":
		input = h[:]
	case "s2k_fo":
		input = []byte(hex.EncodeToString(h[:]))
	default:
		return nil, fmt.Errorf("apiclient/real: unsupported SRP protocol %q", protocol)
	}
	return pbkdf2.Key(input, salt, iterations, 32, sha256.New), nil
}

func padToN(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) >= srpNLenBytes {
		return b
	}
	out := make([]byte, srpNLenBytes)
	copy(out[srpNLenBytes-len(b):], b)
	return out
}

func srpHash(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func srpMultiplier() *big.Int {
	h := sha256.New()
	h.Write(srpN.Bytes())
	h.Write(padToN(srpG))
	return new(big.Int).SetBytes(h.Sum(nil))
}

// srpX implements Apple's `NoUserNameInX` variation: H(salt | H(":" | key)).
func srpX(salt, key []byte) *big.Int {
	inner := sha256.New()
	inner.Write([]byte(":"))
	inner.Write(key)
	outer := sha256.New()
	outer.Write(salt)
	outer.Write(inner.Sum(nil))
	return new(big.Int).SetBytes(outer.Sum(nil))
}

func srpU(A, B *big.Int) *big.Int {
	h := sha256.New()
	h.Write(padToN(A))
	h.Write(padToN(B))
	return new(big.Int).SetBytes(h.Sum(nil))
}

func srpM1(username, salt, A, B, K []byte) []byte {
	hg, hn := srpHash(padToN(srpG)), srpHash(srpN.Bytes())
	xor := make([]byte, len(hg))
	for i := range xor {
		xor[i] = hg[i] ^ hn[i]
	}
	h := sha256.New()
	h.Write(xor)
	h.Write(srpHash(username))
	h.Write(salt)
	h.Write(A)
	h.Write(B)
	h.Write(K)
	return h.Sum(nil)
}

func srpM2(A, M1, K []byte) []byte {
	h := sha256.New()
	h.Write(A)
	h.Write(M1)
	h.Write(K)
	return h.Sum(nil)
}
