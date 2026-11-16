package agent

import (
	"crypto/rand"
	"errors"
	"net/http"
	"os"
)

// LoadOrCreateMasterKey reads the 32-byte key that seals third-party secrets,
// creating it (root-only, 0600) on first start.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 32 {
			return nil, errors.New(path + ": master key must be 32 bytes")
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, k, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// RegisterMasterKey serves the key to the signed panel (方案 §4.8: the panel never reads
// the file itself; it holds the key in memory only).
func RegisterMasterKey(s *Server, key []byte) {
	s.Handle("GET /v1/masterkey", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, map[string][]byte{"key": key})
	})
}
