// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// mp4Box builds an ISO BMFF box with the given name and payload.
func mp4Box(name string, payload ...[]byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+totalLen(payload)))
	copy(b[4:8], name)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

// mp4LargeBox builds an ISO BMFF box using a 64-bit size field.
func mp4LargeBox(name string, payload ...[]byte) []byte {
	b := make([]byte, 16, 16+len(payload))
	binary.BigEndian.PutUint32(b[0:4], 1)
	copy(b[4:8], name)
	binary.BigEndian.PutUint64(b[8:16], uint64(16+totalLen(payload)))
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func totalLen(parts [][]byte) int {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	return n
}

// mp4Data builds an ilst "data" box with the given class and value.
func mp4Data(class int, value []byte) []byte {
	hdr := make([]byte, 8)
	hdr[1] = byte(class >> 16)
	hdr[2] = byte(class >> 8)
	hdr[3] = byte(class)
	return mp4Box("data", hdr, value)
}

// mp4FTYP builds an ftyp box with the given major and compatible brands.
func mp4FTYP(major string, compatible ...string) []byte {
	payload := [][]byte{[]byte(major), {0, 0, 2, 0}}
	for _, brand := range compatible {
		payload = append(payload, []byte(brand))
	}
	return mp4Box("ftyp", payload...)
}

// mp4Item builds a metadata item box containing a single data box.
func mp4Item(name string, class int, value []byte) []byte {
	return mp4Box(name, mp4Data(class, value))
}

// mp4FreeForm builds a free-form "----" atom.
func mp4FreeForm(mean, name string, values ...[]byte) []byte {
	payload := [][]byte{
		mp4Box("mean", make([]byte, 4), []byte(mean)),
		mp4Box("name", make([]byte, 4), []byte(name)),
	}
	for _, v := range values {
		payload = append(payload, mp4Data(1, v))
	}
	return mp4Box("----", payload...)
}

// mp4File assembles a file: ftyp followed by top-level boxes.
func mp4File(ftyp []byte, boxes ...[]byte) []byte {
	b := append([]byte{}, ftyp...)
	for _, box := range boxes {
		b = append(b, box...)
	}
	return b
}

// mp4MoovWith puts the given ilst items in moov/udta/meta/ilst.
func mp4MoovWith(items ...[]byte) []byte {
	ilst := mp4Box("ilst", items...)
	meta := mp4Box("meta", make([]byte, 4), ilst)
	return mp4Box("moov", mp4Box("udta", meta))
}

// mp4Stsd builds an stsd box containing the given sample entries.
func mp4Stsd(entries ...[]byte) []byte {
	return mp4Box("stsd", make([]byte, 4), []byte{0, 0, 0, byte(len(entries))}, concat(entries))
}

// mp4AudioTrack builds a minimal moov/trak/mdia/minf/stbl/stsd hierarchy with
// the given sample entry.
func mp4AudioTrack(sampleEntry []byte) []byte {
	return mp4Box("moov", mp4Box("trak", mp4Box("mdia", mp4Box("minf", mp4Box("stbl", mp4Stsd(sampleEntry))))))
}

func concat(parts [][]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// readMetadata reads metadata from an in-memory MP4 file.
func readMetadata(t *testing.T, b []byte) Metadata {
	t.Helper()
	m, err := ReadFrom(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("ReadFrom: unexpected error: %v", err)
	}
	return m
}

func TestMP4FileTypeFromFTYP(t *testing.T) {
	tests := []struct {
		name       string
		major      string
		compatible []string
		want       FileType
	}{
		{"M4A", "M4A ", []string{"M4A ", "isom"}, M4A},
		{"M4B", "M4B ", []string{"M4B ", "isom"}, M4B},
		{"M4P", "M4P ", []string{"M4P ", "isom"}, M4P},
		{"M4A compatible brand", "isom", []string{"isom", "M4A "}, M4A},
		{"plain mp4", "mp42", []string{"mp42", "isom"}, UnknownFileType},
		{"video", "avc1", []string{"isom", "avc1"}, UnknownFileType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := mp4File(mp4FTYP(tt.major, tt.compatible...), mp4Box("mdat", []byte("audio")))

			_, got, err := Identify(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("Identify: unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Identify file type = %q, want %q", got, tt.want)
			}

			m := readMetadata(t, b)
			if m.FileType() != tt.want {
				t.Errorf("FileType() = %q, want %q", m.FileType(), tt.want)
			}
		})
	}
}

func TestMP4FileTypeFromSTSD(t *testing.T) {
	alacEntry := mp4Box("alac", make([]byte, 28))
	mp4aEntry := mp4Box("mp4a", make([]byte, 28))

	tests := []struct {
		name  string
		ftyp  []byte
		entry []byte
		want  FileType
	}{
		{"ALAC in M4A", mp4FTYP("M4A ", "isom"), alacEntry, ALAC},
		{"ALAC in generic MP4", mp4FTYP("mp42", "isom"), alacEntry, ALAC},
		{"AAC in M4A", mp4FTYP("M4A ", "isom"), mp4aEntry, M4A},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := mp4File(tt.ftyp, mp4Box("mdat", []byte("audio")), mp4AudioTrack(tt.entry))

			_, got, err := Identify(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("Identify: unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Identify file type = %q, want %q", got, tt.want)
			}

			m := readMetadata(t, b)
			if m.FileType() != tt.want {
				t.Errorf("FileType() = %q, want %q", m.FileType(), tt.want)
			}
		})
	}
}

func TestMP4TrackAndDiscNumbers(t *testing.T) {
	// Each value is preceded by two reserved bytes (and the total is
	// followed by two more).
	trkn := []byte{0, 0, 0x01, 0x2C, 0x02, 0x00, 0, 0}
	disk := []byte{0, 0, 0x00, 0x01, 0x00, 0x02, 0, 0}
	b := mp4File(mp4FTYP("M4A "),
		mp4MoovWith(
			mp4Box("trkn", mp4Data(0, trkn)),
			mp4Box("disk", mp4Data(0, disk)),
		),
	)

	m := readMetadata(t, b)
	if got, gotTotal := m.Track(); got != 300 || gotTotal != 512 {
		t.Errorf("Track() = %v of %v, want 300 of 512", got, gotTotal)
	}
	if got, gotTotal := m.Disc(); got != 1 || gotTotal != 2 {
		t.Errorf("Disc() = %v of %v, want 1 of 2", got, gotTotal)
	}
}

func TestMP4ImplicitJPEGCover(t *testing.T) {
	// JPEG magic number, encoded in an implicit (class 0) covr atom.
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	b := mp4File(mp4FTYP("M4A "), mp4MoovWith(mp4Box("covr", mp4Data(0, jpeg))))

	m := readMetadata(t, b)
	p := m.Picture()
	if p == nil {
		t.Fatal("Picture() = nil, want JPEG picture")
	}
	if p.Ext != "jpeg" || p.MIMEType != "image/jpeg" {
		t.Errorf("Picture() Ext, MIMEType = %q, %q, want jpeg, image/jpeg", p.Ext, p.MIMEType)
	}
	if !bytes.Equal(p.Data, jpeg) {
		t.Errorf("Picture() data = %x, want %x", p.Data, jpeg)
	}
}

func TestMP4TagsAfterFreeFormAtom(t *testing.T) {
	b := mp4File(mp4FTYP("M4A "),
		mp4MoovWith(
			mp4FreeForm("com.example.vendor", "Vendor Tag", []byte("value")),
			mp4Item("\xa9nam", 1, []byte("Title After FreeForm")),
		),
	)

	m := readMetadata(t, b)
	if got, want := m.Title(), "Title After FreeForm"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if _, ok := m.Raw()["Vendor Tag"]; ok {
		t.Error("Raw() contains tag from a third-party free-form atom, want it ignored")
	}
}

func TestMP4ZeroSizedBox(t *testing.T) {
	// A box with a zero size extends to the end of its parent.  It must be
	// skipped using the parent's bound (not an unbounded read) and must not
	// prevent the items before it from being read.
	freeForm := mp4FreeForm("com.example.vendor", "Vendor Tag", []byte("value"))
	binary.BigEndian.PutUint32(freeForm[0:4], 0)
	b := mp4File(mp4FTYP("M4A "),
		mp4MoovWith(
			mp4Item("\xa9nam", 1, []byte("Title")),
			freeForm,
		),
	)

	m := readMetadata(t, b)
	if got, want := m.Title(), "Title"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
}

func TestMP4CustomAtomMultipleValues(t *testing.T) {
	b := mp4File(mp4FTYP("M4A "),
		mp4MoovWith(
			mp4FreeForm("com.apple.iTunes", "MusicBrainz Album Type", []byte("album"), []byte("compilation")),
			mp4Item("\xa9nam", 1, []byte("Title")),
		),
	)

	m := readMetadata(t, b)
	// Values of free-form atoms retain their 4-byte locale field, as they
	// always have.
	want := "\x00\x00\x00\x00album;\x00\x00\x00\x00compilation"
	if got := m.Raw()["MusicBrainz Album Type"]; got != want {
		t.Errorf("Raw() custom atom = %v, want %q", got, want)
	}
	if got, want := m.Title(), "Title"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
}

func TestMP4TagsAfterLargeMDAT(t *testing.T) {
	audio := []byte("audio data after a 64-bit mdat box")
	b := mp4File(mp4FTYP("M4A "),
		mp4LargeBox("mdat", audio),
		mp4MoovWith(
			mp4Item("\xa9nam", 1, []byte("Title")),
			mp4Item("trkn", 0, []byte{0, 0, 0x00, 0x03, 0x00, 0x06, 0, 0}),
		),
	)

	m := readMetadata(t, b)
	if got, want := m.Title(), "Title"; got != want {
		t.Errorf("Title() = %q, want %q", got, want)
	}
	if got, gotTotal := m.Track(); got != 3 || gotTotal != 6 {
		t.Errorf("Track() = %v of %v, want 3 of 6", got, gotTotal)
	}
}

func TestMP4SumIndependentOfShell(t *testing.T) {
	audio := []byte("the same audio payload in every shell")

	shells := [][]byte{
		mp4File(mp4FTYP("M4A ", "isom"), mp4Box("free", []byte("padding")), mp4Box("mdat", audio)),
		mp4File(mp4FTYP("M4B ", "isom"), mp4Box("mdat", audio), mp4Box("free", []byte("padding"))),
		mp4File(mp4FTYP("M4P ", "isom"), mp4LargeBox("mdat", audio)),
		// Metadata differences must not affect the sum either.
		mp4File(mp4FTYP("M4A ", "isom"), mp4Box("mdat", audio), mp4MoovWith(mp4Item("\xa9nam", 1, []byte("Title")))),
	}

	var first string
	for i, b := range shells {
		got, err := Sum(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("shell %d: Sum: unexpected error: %v", i, err)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("shell %d: Sum = %q, want %q", i, got, first)
		}
	}
}

func TestMP4SumWithLargeMDAT(t *testing.T) {
	audio := []byte("audio payload")
	b := mp4File(mp4FTYP("M4A "), mp4LargeBox("mdat", audio))

	got, err := Sum(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("Sum: unexpected error: %v", err)
	}

	h, err := SumAtoms(bytes.NewReader(mp4File(mp4FTYP("M4A "), mp4Box("mdat", audio))))
	if err != nil {
		t.Fatalf("SumAtoms: unexpected error: %v", err)
	}
	if got != h {
		t.Errorf("Sum with 64-bit mdat = %q, want %q", got, h)
	}
}
