// Copyright 2024, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildVorbisCommentBlock constructs a FLAC Vorbis comment metadata block
// payload (without the 4-byte block header) from the given raw comment
// strings (e.g. "TITLE=foo", or a malformed "BADCOMMENT").
func buildVorbisCommentBlock(comments []string) []byte {
	var b bytes.Buffer
	vendor := "test-vendor"
	binary.Write(&b, binary.LittleEndian, uint32(len(vendor)))
	b.WriteString(vendor)
	binary.Write(&b, binary.LittleEndian, uint32(len(comments)))
	for _, c := range comments {
		binary.Write(&b, binary.LittleEndian, uint32(len(c)))
		b.WriteString(c)
	}
	return b.Bytes()
}

// buildFLACBlock wraps a payload in a FLAC metadata block header with the
// given block type and last-metadata-block flag.
func buildFLACBlock(blockType byte, last bool, payload []byte) []byte {
	header := blockType
	if last {
		header |= 1 << 7
	}
	out := []byte{header, 0, 0, 0}
	binary.BigEndian.PutUint32(out[0:], uint32(len(payload))|uint32(header)<<24)
	return append(out, payload...)
}

// buildPictureBlock constructs a valid FLAC PICTURE block payload.
func buildPictureBlock(picType byte, mime string, desc string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, int32(picType))
	binary.Write(&b, binary.BigEndian, uint32(len(mime)))
	b.WriteString(mime)
	binary.Write(&b, binary.BigEndian, uint32(len(desc)))
	b.WriteString(desc)
	binary.Write(&b, binary.BigEndian, int32(1))  // width
	binary.Write(&b, binary.BigEndian, int32(1))  // height
	binary.Write(&b, binary.BigEndian, int32(24)) // color depth
	binary.Write(&b, binary.BigEndian, int32(0))  // colors used
	binary.Write(&b, binary.BigEndian, int32(len(data)))
	b.Write(data)
	return b.Bytes()
}

func buildFLAC(blocks ...[]byte) *bytes.Reader {
	var b bytes.Buffer
	b.WriteString("fLaC")
	for _, blk := range blocks {
		b.Write(blk)
	}
	return bytes.NewReader(b.Bytes())
}

// TestFLACMalformedCommentIsSkipped checks that a comment line missing '='
// does not abort parsing and that the remaining tags survive.
func TestFLACMalformedCommentIsSkipped(t *testing.T) {
	payload := buildVorbisCommentBlock([]string{
		"BADCOMMENT",
		"TITLE=Test Title",
		"ARTIST=Test Artist",
	})
	r := buildFLAC(buildFLACBlock(4, true, payload))

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("expected no error for malformed comment, got: %v", err)
	}
	if got := m.Title(); got != "Test Title" {
		t.Errorf("Title: expected %q, got %q", "Test Title", got)
	}
	if got := m.Artist(); got != "Test Artist" {
		t.Errorf("Artist: expected %q, got %q", "Test Artist", got)
	}
}

// TestFLACDuplicateCommentsArePreserved checks that repeated comment keys
// are all retained while single-value accessors keep returning a string.
func TestFLACDuplicateCommentsArePreserved(t *testing.T) {
	payload := buildVorbisCommentBlock([]string{
		"ARTIST=First Artist",
		"ARTIST=Second Artist",
		"TITLE=Test Title",
	})
	r := buildFLAC(buildFLACBlock(4, true, payload))

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.Artist(); got != "First Artist" {
		t.Errorf("Artist (single value): expected %q, got %q", "First Artist", got)
	}
	raw := m.Raw()
	artists, ok := raw["artist"].([]string)
	if !ok {
		t.Fatalf("Raw() artist: expected []string, got %T (%v)", raw["artist"], raw["artist"])
	}
	if len(artists) != 2 || artists[0] != "First Artist" || artists[1] != "Second Artist" {
		t.Errorf("Raw() artist: expected both artists, got %v", artists)
	}
	if got := raw["title"]; got != "Test Title" {
		t.Errorf("Raw() title type/value changed: got %v", got)
	}
}

// TestFLACTrackAndDiscXofY checks x/y splitting plus TRACKTOTAL/DISCTOTAL
// compatibility.
func TestFLACTrackAndDiscXofY(t *testing.T) {
	payload := buildVorbisCommentBlock([]string{
		"TRACKNUMBER=3/12",
		"DISCNUMBER=2/3",
	})
	r := buildFLAC(buildFLACBlock(4, true, payload))

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	track, total := m.Track()
	if track != 3 || total != 12 {
		t.Errorf("Track: expected (3, 12), got (%d, %d)", track, total)
	}
	disc, discTotal := m.Disc()
	if disc != 2 || discTotal != 3 {
		t.Errorf("Disc: expected (2, 3), got (%d, %d)", disc, discTotal)
	}
}

// TestFLACTrackTotalWinsOverXofY checks that an explicit TRACKTOTAL keeps
// taking precedence over the total encoded in TRACKNUMBER.
func TestFLACTrackTotalWinsOverXofY(t *testing.T) {
	payload := buildVorbisCommentBlock([]string{
		"TRACKNUMBER=3/12",
		"TRACKTOTAL=6",
	})
	r := buildFLAC(buildFLACBlock(4, true, payload))

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	track, total := m.Track()
	if track != 3 || total != 6 {
		t.Errorf("Track: expected (3, 6), got (%d, %d)", track, total)
	}
}

// TestFLACPictureBlockOversizedLength checks that a PICTURE block whose
// internal data length disagrees with (and vastly exceeds) the block header
// length fails the block without a huge allocation and without consuming the
// following comment block.
func TestFLACPictureBlockOversizedLength(t *testing.T) {
	pic := buildPictureBlock(3, "image/jpeg", "", bytes.Repeat([]byte("x"), 50))
	// Corrupt the data length field (last 4 bytes of the block) to 20MB.
	binary.BigEndian.PutUint32(pic[len(pic)-4-50:], 20000000)
	if int64(len(pic)) < 79 {
		t.Fatalf("test setup: expected a >=79 byte sample, got %d", len(pic))
	}

	commentPayload := buildVorbisCommentBlock([]string{
		"TITLE=After Picture",
		"ARTIST=After Artist",
	})
	r := buildFLAC(
		buildFLACBlock(6, false, pic),
		buildFLACBlock(4, true, commentPayload),
	)

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("expected malformed picture to be skipped, got error: %v", err)
	}
	if got := m.Title(); got != "After Picture" {
		t.Errorf("Title after bad picture: expected %q, got %q", "After Picture", got)
	}
	if got := m.Artist(); got != "After Artist" {
		t.Errorf("Artist after bad picture: expected %q, got %q", "After Artist", got)
	}
	if p := m.Picture(); p != nil {
		t.Errorf("Picture: expected nil after malformed block, got %+v", p)
	}
}

// TestFLACBadPictureFollowedByGoodPicture checks that one bad PICTURE block
// does not prevent a later well-formed one from being read.
func TestFLACBadPictureFollowedByGoodPicture(t *testing.T) {
	badPic := buildPictureBlock(3, "image/jpeg", "", bytes.Repeat([]byte("x"), 50))
	binary.BigEndian.PutUint32(badPic[len(badPic)-4-50:], 20000000)

	goodData := []byte("PNGDATA")
	goodPic := buildPictureBlock(3, "image/png", "cover", goodData)

	r := buildFLAC(
		buildFLACBlock(6, false, badPic),
		buildFLACBlock(6, true, goodPic),
	)

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := m.Picture()
	if p == nil {
		t.Fatal("expected the good picture to be parsed after the bad one")
	}
	if p.MIMEType != "image/png" || !bytes.Equal(p.Data, goodData) {
		t.Errorf("unexpected picture: %+v", p)
	}
}

// TestFLACBlockShorterThanPayload checks that when a block header's declared
// length is shorter than the data it actually carries, the reader stops at
// the declared boundary and the following block is still parsed.
func TestFLACBlockShorterThanPayload(t *testing.T) {
	fullPayload := buildVorbisCommentBlock([]string{
		"TITLE=First Title",
		"ARTIST=First Artist",
		"GENRE=Jazz",
	})
	// Declare only part of the real payload; the reader must not consume
	// the remaining bytes as the next block header.
	declaredLen := len(fullPayload) - 10
	truncatedBlock := buildFLACBlock(4, false, fullPayload[:declaredLen])

	secondPayload := buildVorbisCommentBlock([]string{
		"ALBUM=Second Album",
	})
	secondBlock := buildFLACBlock(4, true, secondPayload)

	r := buildFLAC(truncatedBlock, secondBlock)
	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("expected truncated block to be tolerated, got: %v", err)
	}
	// The second block starts exactly at the declared boundary and is parsed.
	if got := m.Album(); got != "Second Album" {
		t.Errorf("Album: expected %q from second block, got %q", "Second Album", got)
	}
	// Comments completed before the cut were still kept.
	if got := m.Title(); got != "First Title" {
		t.Errorf("Title: expected %q from first block, got %q", "First Title", got)
	}
}

// TestFLACValidPictureStillParsed is a regression check that a well-formed
// PICTURE block continues to work.
func TestFLACValidPictureStillParsed(t *testing.T) {
	data := []byte("JPEGDATA")
	pic := buildPictureBlock(3, "image/jpeg", "front", data)
	r := buildFLAC(buildFLACBlock(6, true, pic))

	m, err := ReadFLACTags(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p := m.Picture()
	if p == nil {
		t.Fatal("expected picture")
	}
	if p.Ext != "jpg" || p.Description != "front" || !bytes.Equal(p.Data, data) {
		t.Errorf("unexpected picture: %+v", p)
	}
}
