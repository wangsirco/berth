package snapshot

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestSealOpen(t *testing.T) {
	data := make([]byte, 3*chunk+123)
	rand.Read(data)
	var sealed bytes.Buffer
	if err := Seal(&sealed, bytes.NewReader(data), "correct horse"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Open(&out, bytes.NewReader(sealed.Bytes()), "correct horse"); err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("round trip: %v", err)
	}
	if err := Open(&bytes.Buffer{}, bytes.NewReader(sealed.Bytes()), "wrong password"); err != ErrPassword {
		t.Fatalf("wrong password: %v", err)
	}
	cut := sealed.Bytes()[:sealed.Len()-50]
	if err := Open(&bytes.Buffer{}, bytes.NewReader(cut), "correct horse"); err == nil {
		t.Fatal("a truncated file was accepted")
	}
}
