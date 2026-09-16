package transcode

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// frontCoverType is the FLAC/ID3v2 APIC picture type for "Cover (front)".
const frontCoverType = 3

// embeddedPicture returns the raw, still-encoded image bytes of the FLAC
// PICTURE metadata block in path, preferring one tagged as the front cover.
// It returns nil, nil if the file has no attached picture, and is silent
// about anything short of an unreadable file: a malformed or truncated
// block just yields whatever was found before the problem, which is the
// same "best effort" contract the rest of this package uses for artwork.
//
// This exists instead of asking ffmpeg to find the picture itself because
// ffmpeg's FLAC demuxer only recognises a short hardcoded list of MIME
// types for an attached picture (image/jpeg, image/png, ...). Anything
// else - notably image/webp - is dropped with a log warning and no video
// stream is exposed, so ExtractCover would see ffmpeg exit non-zero and
// silently treat a real cover as "no artwork". Reading the block directly
// sidesteps that allowlist: the caller pipes the bytes to ffmpeg's
// image2pipe demuxer, which sniffs the actual format instead of trusting
// the declared MIME string.
func embeddedPicture(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, err
	}
	if string(magic) != "fLaC" {
		return nil, fmt.Errorf("%s: not a FLAC file", path)
	}

	var fallback []byte
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(f, header); err != nil {
			return fallback, nil // truncated stream: return what we have
		}
		last := header[0]&0x80 != 0
		blockType := header[0] & 0x7f
		length := int(header[1])<<16 | int(header[2])<<8 | int(header[3])

		const pictureBlock = 6
		if blockType != pictureBlock {
			if _, err := f.Seek(int64(length), io.SeekCurrent); err != nil {
				return fallback, nil
			}
			if last {
				return fallback, nil
			}
			continue
		}

		body := make([]byte, length)
		if _, err := io.ReadFull(f, body); err != nil {
			return fallback, nil
		}

		if pic, picType, err := parsePictureBlock(body); err == nil {
			if picType == frontCoverType {
				return pic, nil
			}
			if fallback == nil {
				fallback = pic
			}
		}

		if last {
			return fallback, nil
		}
	}
}

// opusencAccepts reports whether opusenc's own picture importer will embed
// pic as-is. opusenc (and its automatic FLAC-picture import) only recognises
// JPEG, PNG and GIF; anything else - a WebP cover being the case that
// actually shows up in real libraries - is silently skipped with just a
// stderr warning ("invalid picture file") and the track ends up with no
// artwork at all, even though opusenc reports success. Sniffing the real
// magic bytes matters more than trusting the FLAC's declared MIME type,
// since that's exactly the field that can't be relied on.
func opusencAccepts(pic []byte) bool {
	switch {
	case len(pic) >= 3 && pic[0] == 0xFF && pic[1] == 0xD8 && pic[2] == 0xFF:
		return true // JPEG
	case len(pic) >= 8 && string(pic[:8]) == "\x89PNG\r\n\x1a\n":
		return true // PNG
	case len(pic) >= 6 && (string(pic[:6]) == "GIF87a" || string(pic[:6]) == "GIF89a"):
		return true // GIF
	default:
		return false
	}
}

// parsePictureBlock extracts the picture type and raw image bytes from a
// FLAC PICTURE metadata block body. Layout:
// https://xiph.org/flac/format.html#metadata_block_picture
func parsePictureBlock(b []byte) ([]byte, uint32, error) {
	read32 := func() (uint32, error) {
		if len(b) < 4 {
			return 0, fmt.Errorf("truncated picture block")
		}
		v := binary.BigEndian.Uint32(b[:4])
		b = b[4:]
		return v, nil
	}
	skip := func(n uint32) error {
		if uint64(len(b)) < uint64(n) {
			return fmt.Errorf("truncated picture block")
		}
		b = b[n:]
		return nil
	}

	picType, err := read32() // picture type
	if err != nil {
		return nil, 0, err
	}
	mimeLen, err := read32()
	if err != nil {
		return nil, 0, err
	}
	if err := skip(mimeLen); err != nil {
		return nil, 0, err
	}
	descLen, err := read32()
	if err != nil {
		return nil, 0, err
	}
	if err := skip(descLen); err != nil {
		return nil, 0, err
	}
	if err := skip(16); err != nil { // width, height, depth, colors: 4 x uint32
		return nil, 0, err
	}
	dataLen, err := read32()
	if err != nil {
		return nil, 0, err
	}
	if uint64(len(b)) < uint64(dataLen) {
		return nil, 0, fmt.Errorf("truncated picture data")
	}
	return b[:dataLen], picType, nil
}
