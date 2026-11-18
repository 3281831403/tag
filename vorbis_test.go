// Copyright 2024, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"runtime"
	"testing"
)

// buildVorbisCommentPayload constructs a raw Vorbis comment stream as found
// inside an OGG packet (after the "\x03vorbis" / "OpusTags" prefix) or a FLAC
// block, from raw comment strings.
func buildVorbisCommentPayload(comments []string) []byte {
	return buildVorbisCommentBlock(comments)
}

// TestVorbisMalformedCommentIsSkipped exercises readVorbisComment directly:
// a line without '=' is skipped while later comments are still stored.
func TestVorbisMalformedCommentIsSkipped(t *testing.T) {
	payload := buildVorbisCommentPayload([]string{
		"NOEQUALSIGN",
		"TITLE=Test Title",
		"ARTIST=Test Artist",
	})
	m := newMetadataVorbis()
	if err := m.readVorbisComment(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.Title(); got != "Test Title" {
		t.Errorf("Title: expected %q, got %q", "Test Title", got)
	}
	if got := m.Artist(); got != "Test Artist" {
		t.Errorf("Artist: expected %q, got %q", "Test Artist", got)
	}
}

// TestVorbisBadBase64PictureIsIgnored checks that an invalid
// METADATA_BLOCK_PICTURE value does not invalidate the other tags.
func TestVorbisBadBase64PictureIsIgnored(t *testing.T) {
	payload := buildVorbisCommentPayload([]string{
		"METADATA_BLOCK_PICTURE=!!!not-base64!!!",
		"TITLE=Test Title",
	})
	m := newMetadataVorbis()
	if err := m.readVorbisComment(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unexpected error for bad base64 picture: %v", err)
	}
	if got := m.Title(); got != "Test Title" {
		t.Errorf("Title: expected %q, got %q", "Test Title", got)
	}
	if p := m.Picture(); p != nil {
		t.Errorf("Picture: expected nil, got %+v", p)
	}
}

// TestVorbisValidBase64PictureStillParsed checks the happy path of an
// embedded METADATA_BLOCK_PICTURE comment.
func TestVorbisValidBase64PictureStillParsed(t *testing.T) {
	pic := buildPictureBlock(3, "image/png", "", []byte("PNGDATA"))
	encoded := base64.StdEncoding.EncodeToString(pic)
	payload := buildVorbisCommentPayload([]string{
		"METADATA_BLOCK_PICTURE=" + encoded,
	})
	m := newMetadataVorbis()
	if err := m.readVorbisComment(bytes.NewReader(payload)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := m.Picture()
	if p == nil || p.MIMEType != "image/png" {
		t.Fatalf("expected parsed picture, got %+v", p)
	}
}

// TestVorbisTruncatedBlockDoesNotAllocateHugePicture checks that a picture
// payload claiming a 20MB data length in a ~79 byte block is rejected without
// allocating the claimed amount.
func TestVorbisTruncatedBlockDoesNotAllocateHugePicture(t *testing.T) {
	pic := buildPictureBlock(3, "image/jpeg", "", bytes.Repeat([]byte("x"), 50))
	binary.BigEndian.PutUint32(pic[len(pic)-4-50:], 20000000)
	if len(pic) < 79 {
		t.Fatalf("test setup: expected >=79 byte sample, got %d", len(pic))
	}

	m := newMetadataVorbis()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := m.readPictureBlock(&io.LimitedReader{R: bytes.NewReader(pic), N: int64(len(pic))})
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("expected error for oversized picture data length, got nil")
	}
	if p := m.Picture(); p != nil {
		t.Errorf("Picture: expected nil after failed parse, got %+v", p)
	}
	// The claimed 20MB length must never be allocated: the failure path
	// only allocates the small fixed-size header reads.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Errorf("allocated %d bytes for a malformed ~79 byte picture block", allocated)
	}
}
