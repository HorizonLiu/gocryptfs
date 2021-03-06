// Package nametransform encrypts and decrypts filenames.
package nametransform

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"path/filepath"
	"syscall"

	"github.com/HorizonLiu/eme"

	"github.com/HorizonLiu/gocryptfs/internal/tlog"
)

const (
	// Like ext4, we allow at most 255 bytes for a file name.
	NameMax = 255
)

// NameTransformer is an interface used to transform filenames.
type NameTransformer interface {
	DecryptName(cipherName string, iv []byte) (string, error)
	EncryptName(plainName string, iv []byte) string
	EncryptAndHashName(name string, iv []byte) (string, error)
	// HashLongName - take the hash of a long string "name" and return
	// "gocryptfs.longname.[sha256]"
	//
	// This function does not do any I/O.
	HashLongName(name string) string
	WriteLongNameAt(dirfd int, hashName string, plainName string) error
	B64EncodeToString(src []byte) string
	B64DecodeString(s string) ([]byte, error)
}

// NameTransform is used to transform filenames.
type NameTransform struct {
	emeCipher *eme.EMECipher
	longNames bool
	// B64 = either base64.URLEncoding or base64.RawURLEncoding, depending
	// on the Raw64 feature flag
	B64 *base64.Encoding
	// Patterns to bypass decryption
	BadnamePatterns []string
}

// New returns a new NameTransform instance.
func New(e *eme.EMECipher, longNames bool, raw64 bool) *NameTransform {
	b64 := base64.URLEncoding
	if raw64 {
		b64 = base64.RawURLEncoding
	}
	return &NameTransform{
		emeCipher: e,
		longNames: longNames,
		B64:       b64,
	}
}

// DecryptName calls decryptName to try and decrypt a base64-encoded encrypted
// filename "cipherName", and failing that checks if it can be bypassed
func (n *NameTransform) DecryptName(cipherName string, iv []byte) (string, error) {
	res, err := n.decryptName(cipherName, iv)
	if err != nil {
		for _, pattern := range n.BadnamePatterns {
			match, err := filepath.Match(pattern, cipherName)
			if err == nil && match { // Pattern should have been validated already
				// Find longest decryptable substring
				// At least 16 bytes due to AES --> at least 22 characters in base64
				nameMin := n.B64.EncodedLen(aes.BlockSize)
				for charpos := len(cipherName) - 1; charpos >= nameMin; charpos-- {
					res, err = n.decryptName(cipherName[:charpos], iv)
					if err == nil {
						return res + cipherName[charpos:] + " GOCRYPTFS_BAD_NAME", nil
					}
				}
				return cipherName + " GOCRYPTFS_BAD_NAME", nil
			}
		}
	}
	return res, err
}

// decryptName decrypts a base64-encoded encrypted filename "cipherName" using the
// initialization vector "iv".
func (n *NameTransform) decryptName(cipherName string, iv []byte) (string, error) {
	bin, err := n.B64.DecodeString(cipherName)
	if err != nil {
		return "", err
	}
	if len(bin) == 0 {
		tlog.Warn.Printf("DecryptName: empty input")
		return "", syscall.EBADMSG
	}
	if len(bin)%aes.BlockSize != 0 {
		tlog.Debug.Printf("DecryptName %q: decoded length %d is not a multiple of 16", cipherName, len(bin))
		return "", syscall.EBADMSG
	}
	bin = n.emeCipher.Decrypt(iv, bin)
	bin, err = unPad16(bin)
	if err != nil {
		tlog.Debug.Printf("DecryptName: unPad16 error detail: %v", err)
		// unPad16 returns detailed errors including the position of the
		// incorrect bytes. Kill the padding oracle by lumping everything into
		// a generic error.
		return "", syscall.EBADMSG
	}
	// A name can never contain a null byte or "/". Make sure we never return those
	// to the kernel, even when we read a corrupted (or fuzzed) filesystem.
	if bytes.Contains(bin, []byte{0}) || bytes.Contains(bin, []byte("/")) {
		return "", syscall.EBADMSG
	}
	// The name should never be "." or "..".
	if bytes.Equal(bin, []byte(".")) || bytes.Equal(bin, []byte("..")) {
		return "", syscall.EBADMSG
	}
	plain := string(bin)
	return plain, err
}

// EncryptName encrypts "plainName", returns a base64-encoded "cipherName64",
// encrypted using EME (https://github.com/HorizonLiu/eme).
//
// This function is exported because in some cases, fusefrontend needs access
// to the full (not hashed) name if longname is used.
func (n *NameTransform) EncryptName(plainName string, iv []byte) (cipherName64 string) {
	bin := []byte(plainName)
	bin = pad16(bin)
	bin = n.emeCipher.Encrypt(iv, bin)
	cipherName64 = n.B64.EncodeToString(bin)
	return cipherName64
}

// B64EncodeToString returns a Base64-encoded string
func (n *NameTransform) B64EncodeToString(src []byte) string {
	return n.B64.EncodeToString(src)
}

// B64DecodeString decodes a Base64-encoded string
func (n *NameTransform) B64DecodeString(s string) ([]byte, error) {
	return n.B64.DecodeString(s)
}
