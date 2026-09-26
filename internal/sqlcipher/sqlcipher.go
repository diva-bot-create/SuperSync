// Package sqlcipher converts SQLCipher 4 databases (as used by rekordbox's
// master.db) to and from plain SQLite files, in pure Go.
//
// SQLCipher 4 defaults: 4096-byte pages, AES-256-CBC per page with a random
// 16-byte IV, HMAC-SHA512 over (ciphertext || IV || page number LE), keys
// derived with PBKDF2-HMAC-SHA512 (256000 iterations) from the passphrase and
// the 16-byte salt stored at the start of the file.
package sqlcipher

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

const (
	PageSize   = 4096
	saltLen    = 16
	ivLen      = 16
	hmacLen    = 64
	Reserve    = ivLen + hmacLen // bytes at the end of each page (already a multiple of the AES block)
	kdfIter    = 256000
	hmacIter   = 2
	hmacSaltXO = 0x3a
)

var sqliteHeader = []byte("SQLite format 3\x00")

// ErrWrongKey means the HMAC check failed on the first page.
var ErrWrongKey = errors.New("couldn't unlock the database (wrong key, or not a SQLCipher 4 file)")

type keys struct {
	salt, enc, mac []byte
}

func deriveKeys(passphrase string, salt []byte) (*keys, error) {
	enc, err := pbkdf2.Key(sha512.New, passphrase, salt, kdfIter, 32)
	if err != nil {
		return nil, err
	}
	macSalt := make([]byte, len(salt))
	for i, b := range salt {
		macSalt[i] = b ^ hmacSaltXO
	}
	mac, err := pbkdf2.Key(sha512.New, string(enc), macSalt, hmacIter, 32)
	if err != nil {
		return nil, err
	}
	return &keys{salt: salt, enc: enc, mac: mac}, nil
}

func (k *keys) pageMAC(data, iv []byte, pgno uint32) []byte {
	m := hmac.New(sha512.New, k.mac)
	m.Write(data)
	m.Write(iv)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], pgno)
	m.Write(n[:])
	return m.Sum(nil)
}

// decryptPage turns one encrypted page (pgno counted from 1) into plaintext.
func (k *keys) decryptPage(page []byte, pgno uint32) ([]byte, error) {
	if len(page) != PageSize {
		return nil, fmt.Errorf("page %d: short page", pgno)
	}
	start := 0
	if pgno == 1 {
		start = saltLen
	}
	data := page[start : PageSize-Reserve]
	iv := page[PageSize-Reserve : PageSize-Reserve+ivLen]
	mac := page[PageSize-Reserve+ivLen:]
	if !hmac.Equal(mac, k.pageMAC(data, iv, pgno)) {
		if pgno == 1 {
			return nil, ErrWrongKey
		}
		return nil, fmt.Errorf("page %d is corrupt (HMAC mismatch)", pgno)
	}
	block, _ := aes.NewCipher(k.enc)
	out := make([]byte, PageSize)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out[start:PageSize-Reserve], data)
	if pgno == 1 {
		copy(out, sqliteHeader)
	}
	return out, nil
}

// encryptPage is the inverse of decryptPage, with a fresh random IV.
func (k *keys) encryptPage(plain []byte, pgno uint32) []byte {
	out := make([]byte, PageSize)
	start := 0
	if pgno == 1 {
		start = saltLen
		copy(out, k.salt)
	}
	iv := out[PageSize-Reserve : PageSize-Reserve+ivLen]
	rand.Read(iv)
	block, _ := aes.NewCipher(k.enc)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[start:PageSize-Reserve], plain[start:PageSize-Reserve])
	copy(out[PageSize-Reserve+ivLen:], k.pageMAC(out[start:PageSize-Reserve], iv, pgno))
	return out
}

// Decrypt reads an encrypted database (plus its -wal file, if present and
// non-empty) and returns the plain SQLite image. The returned salt is needed
// to re-encrypt with the same key material.
func Decrypt(path, passphrase string) (plain []byte, salt []byte, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) < PageSize || len(raw)%PageSize != 0 {
		return nil, nil, fmt.Errorf("%s isn't a SQLCipher database (size %d)", path, len(raw))
	}
	if bytes.HasPrefix(raw, sqliteHeader) {
		return nil, nil, errors.New("database isn't encrypted")
	}
	salt = append([]byte(nil), raw[:saltLen]...)
	k, err := deriveKeys(passphrase, salt)
	if err != nil {
		return nil, nil, err
	}
	n := len(raw) / PageSize
	plain = make([]byte, 0, len(raw))
	for i := 0; i < n; i++ {
		p, err := k.decryptPage(raw[i*PageSize:(i+1)*PageSize], uint32(i+1))
		if err != nil {
			return nil, nil, err
		}
		plain = append(plain, p...)
	}
	if plain, err = applyWAL(path+"-wal", k, plain); err != nil {
		return nil, nil, err
	}
	return plain, salt, nil
}

// Encrypt produces an encrypted image of a plain SQLite database using the
// same passphrase and salt. The plain image must use Reserve bytes of
// reserved space per page (true for anything decrypted by Decrypt).
func Encrypt(plain []byte, passphrase string, salt []byte) ([]byte, error) {
	if len(plain)%PageSize != 0 || !bytes.HasPrefix(plain, sqliteHeader) {
		return nil, errors.New("not a plain SQLite image")
	}
	if int(plain[20]) != Reserve {
		return nil, fmt.Errorf("database has %d reserved bytes per page, need %d", plain[20], Reserve)
	}
	if ps := int(binary.BigEndian.Uint16(plain[16:18])); ps != PageSize {
		return nil, fmt.Errorf("page size %d, need %d", ps, PageSize)
	}
	k, err := deriveKeys(passphrase, salt)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(plain))
	for i := 0; i < len(plain)/PageSize; i++ {
		out = append(out, k.encryptPage(plain[i*PageSize:(i+1)*PageSize], uint32(i+1))...)
	}
	return out, nil
}

// applyWAL replays committed frames from an encrypted write-ahead log.
func applyWAL(path string, k *keys, db []byte) ([]byte, error) {
	w, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(w) < 32) {
		return db, nil
	} else if err != nil {
		return nil, err
	}
	magic := binary.BigEndian.Uint32(w[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return db, nil // not a WAL we understand; the main file alone is consistent
	}
	if ps := binary.BigEndian.Uint32(w[8:12]); ps != PageSize {
		return nil, fmt.Errorf("WAL page size %d", ps)
	}
	salt1, salt2 := binary.BigEndian.Uint32(w[16:20]), binary.BigEndian.Uint32(w[20:24])

	type frame struct {
		pgno uint32
		data []byte
	}
	var pending, committed []frame
	dbPages := uint32(len(db) / PageSize)
	for off := 32; off+24+PageSize <= len(w); off += 24 + PageSize {
		h := w[off : off+24]
		if binary.BigEndian.Uint32(h[8:12]) != salt1 || binary.BigEndian.Uint32(h[12:16]) != salt2 {
			break // frames from an older WAL generation
		}
		pending = append(pending, frame{binary.BigEndian.Uint32(h[0:4]), w[off+24 : off+24+PageSize]})
		if size := binary.BigEndian.Uint32(h[4:8]); size != 0 { // commit frame
			committed = append(committed, pending...)
			pending = pending[:0]
			dbPages = size
		}
	}
	if len(committed) == 0 {
		return db, nil
	}
	if need := int(dbPages) * PageSize; need > len(db) {
		db = append(db, make([]byte, need-len(db))...)
	} else {
		db = db[:need]
	}
	for _, f := range committed {
		if f.pgno == 0 || f.pgno > dbPages {
			continue
		}
		p, err := k.decryptPage(f.data, f.pgno)
		if err != nil {
			return nil, fmt.Errorf("WAL: %w", err)
		}
		copy(db[(f.pgno-1)*PageSize:], p)
	}
	return db, nil
}
