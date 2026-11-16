// Package snapshot seals a panel snapshot (a tar.gz of its database and keys) with a
// password, for moving the panel to another machine (1Panel 的“快照”). Format: the magic
// "BERTHSNAP1", a 16-byte scrypt salt, then chunks of [4-byte length][24-byte nonce]
// [XChaCha20-Poly1305 ciphertext]; the last chunk has length 0 and authenticates the end,
// so a truncated file is detected.
package snapshot

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const magic = "BERTHSNAP1"
const chunk = 1 << 20

var ErrPassword = errors.New("wrong password or damaged file")

func key(pass string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(pass), salt, 1<<15, 8, 1, chacha20poly1305.KeySize)
}

// Seal copies r to w encrypted with pass.
func Seal(w io.Writer, r io.Reader, pass string) error {
	if len(pass) < 8 {
		return errors.New("the password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	k, err := key(pass, salt)
	if err != nil {
		return err
	}
	aead, _ := chacha20poly1305.NewX(k)
	if _, err := w.Write(append([]byte(magic), salt...)); err != nil {
		return err
	}
	buf := make([]byte, chunk)
	var idx uint64
	for {
		n, rerr := io.ReadFull(r, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}
		last := n == 0
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		rand.Read(nonce)
		ad := make([]byte, 9)
		binary.BigEndian.PutUint64(ad, idx)
		if last {
			ad[8] = 1
		}
		ct := aead.Seal(nil, nonce, buf[:n], ad)
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, uint32(n))
		if _, err := w.Write(append(append(hdr, nonce...), ct...)); err != nil {
			return err
		}
		if last {
			return nil
		}
		idx++
	}
}

// Open copies the decrypted contents of r to w.
func Open(w io.Writer, r io.Reader, pass string) error {
	head := make([]byte, len(magic)+16)
	if _, err := io.ReadFull(r, head); err != nil || string(head[:len(magic)]) != magic {
		return errors.New("not a Berth snapshot")
	}
	k, err := key(pass, head[len(magic):])
	if err != nil {
		return err
	}
	aead, _ := chacha20poly1305.NewX(k)
	var idx uint64
	for {
		hdr := make([]byte, 4+chacha20poly1305.NonceSizeX)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return ErrPassword
		}
		n := binary.BigEndian.Uint32(hdr)
		if n > chunk {
			return ErrPassword
		}
		ct := make([]byte, int(n)+aead.Overhead())
		if _, err := io.ReadFull(r, ct); err != nil {
			return ErrPassword
		}
		ad := make([]byte, 9)
		binary.BigEndian.PutUint64(ad, idx)
		if n == 0 {
			ad[8] = 1
		}
		pt, err := aead.Open(nil, hdr[4:], ct, ad)
		if err != nil {
			return ErrPassword
		}
		if n == 0 {
			return nil
		}
		if _, err := w.Write(pt); err != nil {
			return err
		}
		idx++
	}
}
