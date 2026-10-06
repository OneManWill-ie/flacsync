package transcode

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// flacWithPicture builds a minimal but structurally valid FLAC file with one
// PICTURE block of the given type and returns its path, bytes and the offset
// of the picture type field.
func flacWithPicture(t *testing.T, picType uint32) (string, []byte, int) {
	t.Helper()

	var body []byte
	var tmp [4]byte
	put32 := func(v uint32) {
		binary.BigEndian.PutUint32(tmp[:], v)
		body = append(body, tmp[:]...)
	}
	put32(picType)
	put32(10) // MIME length
	body = append(body, "image/jpeg"...)
	put32(0) // description length
	body = append(body, make([]byte, 16)...)
	put32(3)
	body = append(body, "abc"...)

	var b []byte
	b = append(b, "fLaC"...)
	b = append(b, 0x00, 0x00, 0x00, 0x22) // STREAMINFO, not last, 34 bytes
	b = append(b, make([]byte, 34)...)
	b = append(b, 0x86, byte(len(body)>>16), byte(len(body)>>8), byte(len(body))) // PICTURE, last
	typeOff := len(b)
	b = append(b, body...)

	path := filepath.Join(t.TempDir(), "test.flac")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, b, typeOff
}

func TestPromoteFrontCoverRewritesOtherType(t *testing.T) {
	path, raw, off := flacWithPicture(t, 0)

	if err := promoteFrontCover(path); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(raw) {
		t.Fatalf("length changed: %d -> %d", len(raw), len(out))
	}
	if got := binary.BigEndian.Uint32(out[off:]); got != frontCoverType {
		t.Fatalf("picture type = %d, want %d", got, frontCoverType)
	}
	if string(out[len(out)-3:]) != "abc" {
		t.Fatal("picture data was disturbed")
	}
}

func TestPromoteFrontCoverKeepsExplicitType(t *testing.T) {
	const backCover = 4
	path, _, off := flacWithPicture(t, backCover)

	if err := promoteFrontCover(path); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(out[off:]); got != backCover {
		t.Fatalf("picture type = %d, want %d (explicit types must not change)", got, backCover)
	}
}
