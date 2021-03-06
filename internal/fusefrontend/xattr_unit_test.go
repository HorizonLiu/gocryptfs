package fusefrontend

// This file is named "xattr_unit_test.go" because there is also a
// "xattr_integration_test.go" in the test/xattr package.

import (
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/HorizonLiu/gocryptfs/internal/contentenc"
	"github.com/HorizonLiu/gocryptfs/internal/cryptocore"
	"github.com/HorizonLiu/gocryptfs/internal/nametransform"
)

func newTestFS(args Args) *RootNode {
	// Init crypto backend
	key := make([]byte, cryptocore.KeyLen)
	cCore := cryptocore.New(key, cryptocore.BackendGoGCM, contentenc.DefaultIVBits, true, false)
	cEnc := contentenc.New(cCore, contentenc.DefaultBS, false)
	n := nametransform.New(cCore.EMECipher, true, true)
	rn := NewRootNode(args, cEnc, n)
	oneSec := time.Second
	options := &fs.Options{
		EntryTimeout: &oneSec,
		AttrTimeout:  &oneSec,
	}
	fs.NewNodeFS(rn, options)
	return rn
}

func TestEncryptDecryptXattrName(t *testing.T) {
	fs := newTestFS(Args{})
	attr1 := "user.foo123456789"
	cAttr := fs.encryptXattrName(attr1)
	t.Logf("cAttr=%v", cAttr)
	attr2, err := fs.decryptXattrName(cAttr)
	if attr1 != attr2 || err != nil {
		t.Fatalf("Decrypt mismatch: %v != %v", attr1, attr2)
	}
}
