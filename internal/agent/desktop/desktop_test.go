package desktop

import (
	"encoding/hex"
	"testing"
)

// vncpasswd -f <<< "password" | xxd -p  →  dbd83cfd727a1458
func TestVNCPassword(t *testing.T) {
	if got := hex.EncodeToString(VNCPassword("password")); got != "dbd83cfd727a1458" {
		t.Fatal(got)
	}
}
