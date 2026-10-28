package real

import (
	"bytes"
	"testing"
)

func TestDerivePasswordSupportsAppleProtocols(t *testing.T) {
	salt := []byte("test-salt")

	s2k, err := derivePassword("correct horse battery staple", salt, 1_000, "s2k")
	if err != nil {
		t.Fatalf("derivePassword(s2k): %v", err)
	}
	s2kFO, err := derivePassword("correct horse battery staple", salt, 1_000, "s2k_fo")
	if err != nil {
		t.Fatalf("derivePassword(s2k_fo): %v", err)
	}
	if len(s2k) != 32 || len(s2kFO) != 32 {
		t.Fatalf("derived lengths = %d, %d; want 32, 32", len(s2k), len(s2kFO))
	}
	if bytes.Equal(s2k, s2kFO) {
		t.Fatal("s2k and s2k_fo derived keys must differ")
	}
	if _, err := derivePassword("password", salt, 1, "unknown"); err == nil {
		t.Fatal("derivePassword accepted an unknown Apple SRP protocol")
	}
}

func TestSRPClientRejectsInvalidServerPublicValue(t *testing.T) {
	client, err := newSRPClient()
	if err != nil {
		t.Fatalf("newSRPClient(): %v", err)
	}
	if err := client.processChallenge([]byte("user@example.com"), make([]byte, 32), []byte("salt"), padToN(srpN)); err == nil {
		t.Fatal("processChallenge accepted B=N; RFC 5054 requires B in [1,N)")
	}
}
