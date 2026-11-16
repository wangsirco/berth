package panel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"github.com/wangsirco/berth/internal/errs"
)

// seal encrypts third-party secrets with the master key (AES-256-GCM, nonce prefixed).
func (a *App) seal(plain []byte) ([]byte, error) {
	g, err := a.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, nil), nil
}

func (a *App) open(enc []byte) ([]byte, error) {
	g, err := a.gcm()
	if err != nil {
		return nil, err
	}
	if len(enc) < g.NonceSize() {
		return nil, errors.New("sealed data too short")
	}
	return g.Open(nil, enc[:g.NonceSize()], enc[g.NonceSize():], nil)
}

func (a *App) gcm() (cipher.AEAD, error) {
	if len(a.masterKey) != 32 {
		return nil, errs.New(500, "panel.no_master_key")
	}
	b, err := aes.NewCipher(a.masterKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}
