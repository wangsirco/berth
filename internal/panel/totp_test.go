package panel

import (
	"testing"
	"time"
)

func TestTOTPKnownVector(t *testing.T) {
	// RFC 6238 SHA1 向量：secret "12345678901234567890"，T=59s → 94287082（8 位），6 位取后 6 位 287082
	secret := []byte("12345678901234567890")
	if got := totpCode(secret, time.Unix(59, 0)); got != "287082" {
		t.Fatalf("got %s", got)
	}
}

func TestTOTPVerifyWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_700_000_000, 0)
	code := totpCode(secret, now.Add(-30*time.Second))
	step, ok := totpVerify(secret, code, now, 0)
	if !ok {
		t.Fatal("previous step should be accepted")
	}
	if _, ok := totpVerify(secret, code, now, step); ok {
		t.Fatal("replay accepted")
	}
}
