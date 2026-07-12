package sign

import (
	"errors"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	s := New("topsecret")
	exp := int64(1_000_000)
	sig := s.Sign("abc123", 2, exp)

	if err := s.Verify("abc123", 2, exp, sig, exp-1); err != nil {
		t.Fatalf("valid sig should verify: %v", err)
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	s := New("topsecret")
	exp := int64(1_000_000)
	sig := s.Sign("abc123", 2, exp)

	// Wrong hash, index, and exp each break the signature.
	for _, tc := range []struct {
		hash string
		idx  int
		exp  int64
	}{
		{"abc124", 2, exp},
		{"abc123", 3, exp},
		{"abc123", 2, exp + 1},
	} {
		if err := s.Verify(tc.hash, tc.idx, tc.exp, sig, exp-1); !errors.Is(err, ErrBadSig) {
			t.Errorf("expected ErrBadSig for %+v, got %v", tc, err)
		}
	}

	// Different secret.
	other := New("different")
	if err := other.Verify("abc123", 2, exp, sig, exp-1); !errors.Is(err, ErrBadSig) {
		t.Errorf("expected ErrBadSig for wrong secret, got %v", err)
	}
}

func TestVerifyExpiry(t *testing.T) {
	s := New("topsecret")
	exp := int64(1_000_000)
	sig := s.Sign("abc123", 0, exp)

	if err := s.Verify("abc123", 0, exp, sig, exp); !errors.Is(err, ErrExpired) {
		t.Errorf("exp == now should be expired, got %v", err)
	}
	if err := s.Verify("abc123", 0, exp, sig, exp+5000); !errors.Is(err, ErrExpired) {
		t.Errorf("past exp should be expired, got %v", err)
	}
	// A bad signature on an expired URL should report bad sig, not expiry.
	if err := s.Verify("abc123", 0, exp, "deadbeef", exp+5000); !errors.Is(err, ErrBadSig) {
		t.Errorf("bad sig should take precedence, got %v", err)
	}
}
