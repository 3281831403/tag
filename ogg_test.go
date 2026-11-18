// Copyright 2024, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildOGGPage wraps a single complete packet into an OGG page with a valid
// CRC checksum. Packet lengths above 255 are split across segments.
func buildOGGPage(serial, sequence uint32, flags byte, packet []byte) []byte {
	var segments []byte
	remaining := packet
	for len(remaining) >= 255 {
		segments = append(segments, 255)
		remaining = remaining[255:]
	}
	if len(remaining) > 0 || len(segments) == 0 {
		segments = append(segments, byte(len(remaining)))
	}

	header := make([]byte, 27)
	copy(header[0:4], "OggS")
	header[4] = 0 // version
	header[5] = flags
	binary.LittleEndian.PutUint64(header[6:14], 0) // granule position
	binary.LittleEndian.PutUint32(header[14:18], serial)
	binary.LittleEndian.PutUint32(header[18:22], sequence)
	// CRC at [22:26] left zero while checksumming
	header[26] = byte(len(segments))

	crc := oggCRCUpdate(0, oggCRC32Poly04c11db7, header)
	crc = oggCRCUpdate(crc, oggCRC32Poly04c11db7, segments)
	crc = oggCRCUpdate(crc, oggCRC32Poly04c11db7, packet)
	binary.LittleEndian.PutUint32(header[22:26], crc)

	var out bytes.Buffer
	out.Write(header)
	out.Write(segments)
	out.Write(packet)
	return out.Bytes()
}

// buildVorbisPacket prefixes a raw Vorbis comment payload with the
// "\x03vorbis" framing expected inside an OGG Vorbis comment packet.
func buildVorbisPacket(comments []string) []byte {
	var out bytes.Buffer
	out.Write(vorbisCommentPrefix)
	out.Write(buildVorbisCommentPayload(comments))
	return out.Bytes()
}

// TestOGGMalformedCommentIsSkipped checks the whole ReadOGGTags path: a
// comment line without '=' must not cause the read to fail and the other
// tags must be returned.
func TestOGGMalformedCommentIsSkipped(t *testing.T) {
	packet := buildVorbisPacket([]string{
		"BADCOMMENT",
		"TITLE=Test Title",
		"ARTIST=Test Artist",
	})
	stream := bytes.NewReader(append(
		buildOGGPage(1, 0, 0x02, []byte("\x01vorbis")), // bos identification header
		buildOGGPage(1, 1, 0x00, packet)...,
	))

	m, err := ReadOGGTags(stream)
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

// TestOGGDuplicateCommentsArePreserved checks repeated keys survive in OGG
// while the single-value accessors keep their string return type.
func TestOGGDuplicateCommentsArePreserved(t *testing.T) {
	packet := buildVorbisPacket([]string{
		"ARTIST=First Artist",
		"ARTIST=Second Artist",
	})
	stream := bytes.NewReader(append(
		buildOGGPage(1, 0, 0x02, []byte("\x01vorbis")),
		buildOGGPage(1, 1, 0x00, packet)...,
	))

	m, err := ReadOGGTags(stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := m.Artist(); got != "First Artist" {
		t.Errorf("Artist (single value): expected %q, got %q", "First Artist", got)
	}
	artists, ok := m.Raw()["artist"].([]string)
	if !ok || len(artists) != 2 || artists[1] != "Second Artist" {
		t.Errorf("Raw() artist: expected both artists, got %v", m.Raw()["artist"])
	}
}

// TestOGGTrackXofY checks x/y parsing through the OGG packet path.
func TestOGGTrackXofY(t *testing.T) {
	packet := buildVorbisPacket([]string{
		"TRACKNUMBER=3/12",
		"DISCNUMBER=2/3",
	})
	stream := bytes.NewReader(append(
		buildOGGPage(1, 0, 0x02, []byte("\x01vorbis")),
		buildOGGPage(1, 1, 0x00, packet)...,
	))

	m, err := ReadOGGTags(stream)
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

// TestOGGBadBase64PictureIsIgnored checks a bad METADATA_BLOCK_PICTURE
// comment does not fail the OGG read or drop the other tags.
func TestOGGBadBase64PictureIsIgnored(t *testing.T) {
	packet := buildVorbisPacket([]string{
		"METADATA_BLOCK_PICTURE=!!!not-base64!!!",
		"TITLE=Test Title",
	})
	stream := bytes.NewReader(append(
		buildOGGPage(1, 0, 0x02, []byte("\x01vorbis")),
		buildOGGPage(1, 1, 0x00, packet)...,
	))

	m, err := ReadOGGTags(stream)
	if err != nil {
		t.Fatalf("expected no error for bad base64 picture, got: %v", err)
	}
	if got := m.Title(); got != "Test Title" {
		t.Errorf("Title: expected %q, got %q", "Test Title", got)
	}
	if p := m.Picture(); p != nil {
		t.Errorf("Picture: expected nil, got %+v", p)
	}
}
