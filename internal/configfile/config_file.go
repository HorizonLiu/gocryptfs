// Package configfile reads and writes gocryptfs.conf does the key
// wrapping.
package configfile

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"syscall"

	"os"

	"github.com/HorizonLiu/gocryptfs/internal/contentenc"
	"github.com/HorizonLiu/gocryptfs/internal/cryptocore"
	"github.com/HorizonLiu/gocryptfs/internal/exitcodes"
	"github.com/HorizonLiu/gocryptfs/internal/tlog"
)

const (
	// ConfDefaultName is the default configuration file name.
	// The dot "." is not used in base64url (RFC4648), hence
	// we can never clash with an encrypted file.
	ConfDefaultName = "gocryptfs.conf"
	// ConfReverseName is the default configuration file name in reverse mode,
	// the config file gets stored next to the plain-text files. Make it hidden
	// (start with dot) to not annoy the user.
	ConfReverseName = ".gocryptfs.reverse.conf"
)

// FIDO2Params is a structure for storing FIDO2 parameters.
type FIDO2Params struct {
	// FIDO2 credential
	CredentialID []byte
	// FIDO2 hmac-secret salt
	HMACSalt []byte
}

// ConfFile is the content of a config file.
type ConfFile struct {
	// Creator is the gocryptfs version string.
	// This only documents the config file for humans who look at it. The actual
	// technical info is contained in FeatureFlags.
	Creator string
	// EncryptedKey holds an encrypted AES key, unlocked using a password
	// hashed with scrypt
	EncryptedKey []byte
	// ScryptObject stores parameters for scrypt hashing (key derivation)
	ScryptObject ScryptKDF
	// Version is the On-Disk-Format version this filesystem uses
	Version uint16
	// FeatureFlags is a list of feature flags this filesystem has enabled.
	// If gocryptfs encounters a feature flag it does not support, it will refuse
	// mounting. This mechanism is analogous to the ext4 feature flags that are
	// stored in the superblock.
	FeatureFlags []string
	// FIDO2 parameters
	FIDO2 FIDO2Params
	// Filename is the name of the config file. Not exported to JSON.
	filename string
}

// randBytesDevRandom gets "n" random bytes from /dev/random or panics
func randBytesDevRandom(n int) []byte {
	f, err := os.Open("/dev/random")
	if err != nil {
		log.Panic("Failed to open /dev/random: " + err.Error())
	}
	defer f.Close()
	b := make([]byte, n)
	_, err = io.ReadFull(f, b)
	if err != nil {
		log.Panic("Failed to read random bytes: " + err.Error())
	}
	return b
}

// Create - create a new config with a random key encrypted with
// "password" and write it to "filename".
// Uses scrypt with cost parameter logN.
func Create(filename string, password []byte, plaintextNames bool,
	logN int, creator string, aessiv bool, devrandom bool, fido2CredentialID []byte, fido2HmacSalt []byte) error {
	var cf ConfFile
	cf.filename = filename
	cf.Creator = creator
	cf.Version = contentenc.CurrentVersion

	// Set feature flags
	cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagGCMIV128])
	cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagHKDF])
	if plaintextNames {
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagPlaintextNames])
	} else {
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagDirIV])
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagEMENames])
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagLongNames])
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagRaw64])
	}
	if aessiv {
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagAESSIV])
	}
	if len(fido2CredentialID) > 0 {
		cf.FeatureFlags = append(cf.FeatureFlags, knownFlags[FlagFIDO2])
		cf.FIDO2.CredentialID = fido2CredentialID
		cf.FIDO2.HMACSalt = fido2HmacSalt
	}
	{
		// Generate new random master key
		var key []byte
		if devrandom {
			key = randBytesDevRandom(cryptocore.KeyLen)
		} else {
			key = cryptocore.RandBytes(cryptocore.KeyLen)
		}
		tlog.PrintMasterkeyReminder(key)
		// Encrypt it using the password
		// This sets ScryptObject and EncryptedKey
		// Note: this looks at the FeatureFlags, so call it AFTER setting them.
		cf.EncryptKey(key, password, logN)
		for i := range key {
			key[i] = 0
		}
		// key runs out of scope here
	}
	// Write file to disk
	return cf.WriteFile()
}

// LoadAndDecrypt - read config file from disk and decrypt the
// contained key using "password".
// Returns the decrypted key and the ConfFile object
//
// If "password" is empty, the config file is read
// but the key is not decrypted (returns nil in its place).
func LoadAndDecrypt(filename string, password []byte) ([]byte, *ConfFile, error) {
	cf, err := Load(filename)
	if err != nil {
		return nil, nil, err
	}
	if len(password) == 0 {
		// We have validated the config file, but without a password we cannot
		// decrypt the master key. Return only the parsed config.
		return nil, cf, nil
		// TODO: Make this an error in gocryptfs v1.7. All code should now call
		// Load() instead of calling LoadAndDecrypt() with an empty password.
	}

	// Decrypt the masterkey using the password
	key, err := cf.DecryptMasterKey(password)
	if err != nil {
		return nil, nil, err
	}

	return key, cf, err
}

// Load loads and parses the config file at "filename".
func Load(filename string) (*ConfFile, error) {
	var cf ConfFile
	cf.filename = filename

	// Read from disk
	js, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	if len(js) == 0 {
		return nil, fmt.Errorf("Config file is empty")
	}

	// Unmarshal
	err = json.Unmarshal(js, &cf)
	if err != nil {
		tlog.Warn.Printf("Failed to unmarshal config file")
		return nil, err
	}

	if cf.Version != contentenc.CurrentVersion {
		return nil, fmt.Errorf("Unsupported on-disk format %d", cf.Version)
	}

	// Check that all set feature flags are known
	for _, flag := range cf.FeatureFlags {
		if !cf.isFeatureFlagKnown(flag) {
			return nil, fmt.Errorf("Unsupported feature flag %q", flag)
		}
	}

	// Check that all required feature flags are set
	var requiredFlags []flagIota
	if cf.IsFeatureFlagSet(FlagPlaintextNames) {
		requiredFlags = requiredFlagsPlaintextNames
	} else {
		requiredFlags = requiredFlagsNormal
	}
	deprecatedFs := false
	for _, i := range requiredFlags {
		if !cf.IsFeatureFlagSet(i) {
			fmt.Fprintf(os.Stderr, "Required feature flag %q is missing\n", knownFlags[i])
			deprecatedFs = true
		}
	}
	if deprecatedFs {
		fmt.Fprintf(os.Stderr, tlog.ColorYellow+`
    The filesystem was created by gocryptfs v0.6 or earlier. This version of
    gocryptfs can no longer mount the filesystem.
    Please download gocryptfs v0.11 and upgrade your filesystem,
    see https://github.com/HorizonLiu/gocryptfs/wiki/Upgrading for instructions.

    If you have trouble upgrading, join the discussion at
    https://github.com/HorizonLiu/gocryptfs/issues/29 .

`+tlog.ColorReset)

		return nil, exitcodes.NewErr("Deprecated filesystem", exitcodes.DeprecatedFS)
	}

	// All good
	return &cf, nil
}

// DecryptMasterKey decrypts the masterkey stored in cf.EncryptedKey using
// password.
func (cf *ConfFile) DecryptMasterKey(password []byte) (masterkey []byte, err error) {
	// Generate derived key from password
	scryptHash := cf.ScryptObject.DeriveKey(password)

	// Unlock master key using password-based key
	useHKDF := cf.IsFeatureFlagSet(FlagHKDF)
	ce := getKeyEncrypter(scryptHash, useHKDF)

	tlog.Warn.Enabled = false // Silence DecryptBlock() error messages on incorrect password
	masterkey, err = ce.DecryptBlock(cf.EncryptedKey, 0, nil)
	tlog.Warn.Enabled = true

	// Purge scrypt-derived key
	for i := range scryptHash {
		scryptHash[i] = 0
	}
	scryptHash = nil
	ce.Wipe()
	ce = nil

	if err != nil {
		tlog.Warn.Printf("failed to unlock master key: %s", err.Error())
		return nil, exitcodes.NewErr("Password incorrect.", exitcodes.PasswordIncorrect)
	}
	return masterkey, nil
}

// EncryptKey - encrypt "key" using an scrypt hash generated from "password"
// and store it in cf.EncryptedKey.
// Uses scrypt with cost parameter logN and stores the scrypt parameters in
// cf.ScryptObject.
func (cf *ConfFile) EncryptKey(key []byte, password []byte, logN int) {
	// Generate scrypt-derived key from password
	cf.ScryptObject = NewScryptKDF(logN)
	scryptHash := cf.ScryptObject.DeriveKey(password)

	// Lock master key using password-based key
	useHKDF := cf.IsFeatureFlagSet(FlagHKDF)
	ce := getKeyEncrypter(scryptHash, useHKDF)
	cf.EncryptedKey = ce.EncryptBlock(key, 0, nil)

	// Purge scrypt-derived key
	for i := range scryptHash {
		scryptHash[i] = 0
	}
	scryptHash = nil
	ce.Wipe()
	ce = nil
}

// WriteFile - write out config in JSON format to file "filename.tmp"
// then rename over "filename".
// This way a password change atomically replaces the file.
func (cf *ConfFile) WriteFile() error {
	tmp := cf.filename + ".tmp"
	// 0400 permissions: gocryptfs.conf should be kept secret and never be written to.
	fd, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	js, err := json.MarshalIndent(cf, "", "\t")
	if err != nil {
		return err
	}
	// For convenience for the user, add a newline at the end.
	js = append(js, '\n')
	_, err = fd.Write(js)
	if err != nil {
		return err
	}
	err = fd.Sync()
	if err != nil {
		// This can happen on network drives: FRITZ.NAS mounted on MacOS returns
		// "operation not supported": https://github.com/HorizonLiu/gocryptfs/issues/390
		tlog.Warn.Printf("Warning: fsync failed: %v", err)
		// Try sync instead
		syscall.Sync()
	}
	err = fd.Close()
	if err != nil {
		return err
	}
	err = os.Rename(tmp, cf.filename)
	return err
}

// getKeyEncrypter is a helper function that returns the right ContentEnc
// instance for the "useHKDF" setting.
func getKeyEncrypter(scryptHash []byte, useHKDF bool) *contentenc.ContentEnc {
	IVLen := 96
	// gocryptfs v1.2 and older used 96-bit IVs for master key encryption.
	// v1.3 adds the "HKDF" feature flag, which also enables 128-bit nonces.
	if useHKDF {
		IVLen = contentenc.DefaultIVBits
	}
	cc := cryptocore.New(scryptHash, cryptocore.BackendGoGCM, IVLen, useHKDF, false)
	ce := contentenc.New(cc, 4096, false)
	return ce
}
