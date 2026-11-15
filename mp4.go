// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

var atomTypes = map[int]string{
	0:  "implicit", // automatic based on atom name
	1:  "text",
	13: "jpeg",
	14: "png",
	21: "uint8",
}

// NB: atoms does not include "----", this is handled separately
var atoms = atomNames(map[string]string{
	"\xa9alb": "album",
	"\xa9art": "artist",
	"\xa9ART": "artist",
	"aART":    "album_artist",
	"\xa9day": "year",
	"\xa9nam": "title",
	"\xa9gen": "genre",
	"trkn":    "track",
	"\xa9wrt": "composer",
	"\xa9too": "encoder",
	"cprt":    "copyright",
	"covr":    "picture",
	"\xa9grp": "grouping",
	"keyw":    "keyword",
	"\xa9lyr": "lyrics",
	"\xa9cmt": "comment",
	"tmpo":    "tempo",
	"cpil":    "compilation",
	"disk":    "disc",
})

var means = map[string]bool{
	"com.apple.iTunes":          true,
	"com.mixedinkey.mixedinkey": true,
	"com.serato.dj":             true,
}

// Detect PNG and JPEG images if "implicit" class is used
var (
	pngHeader  = []byte{137, 80, 78, 71, 13, 10, 26, 10}
	jpegHeader = []byte{0xFF, 0xD8, 0xFF}
)

type atomNames map[string]string

func (f atomNames) Name(n string) []string {
	res := make([]string, 1)
	for k, v := range f {
		if v == n {
			res = append(res, k)
		}
	}
	return res
}

// metadataMP4 is the implementation of Metadata for MP4 tag (atom) data.
type metadataMP4 struct {
	fileType FileType
	data     map[string]interface{}
}

// ReadAtoms reads MP4 metadata atoms from the io.ReadSeeker into a Metadata, returning
// non-nil error if there was a problem.
func ReadAtoms(r io.ReadSeeker) (Metadata, error) {
	m := metadataMP4{
		data:     make(map[string]interface{}),
		fileType: UnknownFileType,
	}
	// The file type is determined by the ftyp and stsd boxes, so inspect
	// them before reading the metadata.  A file whose type cannot be
	// determined can still have its metadata read.
	m.fileType = mp4FileType(r)
	err := m.readAtoms(r)
	return m, err
}

func (m *metadataMP4) readAtoms(r io.ReadSeeker) error {
	end, err := readSeekerEnd(r)
	if err != nil {
		return err
	}
	return m.readBoxes(r, end, 0)
}

// maxBoxDepth bounds the nesting of boxes which are traversed, so that a
// maliciously crafted file cannot exhaust the stack.
const maxBoxDepth = 32

// readBoxes reads a sequence of ISO BMFF boxes ending at end.  Metadata
// containers are traversed recursively; all other boxes are skipped by their
// declared size (including 64-bit and "to end of file" sizes).
func (m *metadataMP4) readBoxes(r io.ReadSeeker, end int64, depth int) error {
	if depth > maxBoxDepth {
		return fmt.Errorf("atom nesting exceeds %d levels", maxBoxDepth)
	}
	return walkBoxes(r, end, func(name string, payloadStart, boxEnd int64) error {
		switch name {
		case "moov", "udta":
			if _, err := r.Seek(payloadStart, io.SeekStart); err != nil {
				return err
			}
			return m.readBoxes(r, boxEnd, depth+1)

		case "meta":
			// meta is a full box: version (1 byte) + flags (3 bytes).
			if boxEnd-payloadStart < 4 {
				return nil
			}
			if _, err := r.Seek(payloadStart+4, io.SeekStart); err != nil {
				return err
			}
			return m.readBoxes(r, boxEnd, depth+1)

		case "ilst":
			if _, err := r.Seek(payloadStart, io.SeekStart); err != nil {
				return err
			}
			return m.readItems(r, boxEnd)
		}
		return nil
	})
}

// readItems reads the atoms contained in an ilst box.  Each child is a
// metadata item whose payload contains one or more data boxes.
func (m *metadataMP4) readItems(r io.ReadSeeker, end int64) error {
	return walkBoxes(r, end, func(name string, payloadStart, boxEnd int64) error {
		if name == "----" {
			mean, customName, data, err := readCustomAtom(r, payloadStart, boxEnd)
			if err != nil {
				return err
			}
			if !means[mean] || customName == "" || len(data) == 0 {
				return nil
			}
			// Add delimiter if multiple data fields.
			m.data[customName] = strings.Join(data, ";")
			return nil
		}

		if _, ok := atoms[name]; !ok {
			return nil
		}
		return m.readAtomData(r, name, payloadStart, boxEnd)
	})
}

// readAtomData reads the data box (or boxes) of a metadata item.
func (m *metadataMP4) readAtomData(r io.ReadSeeker, name string, start, end int64) error {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	return walkBoxes(r, end, func(child string, payloadStart, boxEnd int64) error {
		if child != "data" {
			return nil
		}

		size := boxEnd - payloadStart
		if size < 8 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, for atom version and flags, got %d", 8, size)
		}
		b, err := readBytes(r, uint(size))
		if err != nil {
			return err
		}

		// 4: atom version (1 byte) + atom flags (3 bytes)
		// 4: NULL (usually locale indicator)
		class := getInt(b[1:4])

		contentType, ok := atomTypes[class]
		if !ok {
			return fmt.Errorf("invalid content type: %v (%x) (%x)", class, b[1:4], b)
		}
		b = b[8:]

		if name == "trkn" || name == "disk" {
			if len(b) < 6 {
				return fmt.Errorf("invalid encoding: expected at least %d bytes, for track and disk numbers, got %d", 6, len(b))
			}

			// Number and total are 16-bit integers, each preceded by
			// (or followed by) two reserved bytes.
			m.data[name] = getInt(b[2:4])
			m.data[name+"_count"] = getInt(b[4:6])
			return nil
		}

		if contentType == "implicit" && name == "covr" {
			switch {
			case bytes.HasPrefix(b, pngHeader):
				contentType = "png"
			case bytes.HasPrefix(b, jpegHeader):
				contentType = "jpeg"
			}
		}

		var data interface{}
		switch contentType {
		case "implicit":
			if _, ok := atoms[name]; ok {
				return fmt.Errorf("unhandled implicit content type for required atom: %q", name)
			}
			return nil

		case "text":
			data = string(b)

		case "uint8":
			if len(b) < 1 {
				return fmt.Errorf("invalid encoding: expected at least %d bytes, for integer tag data, got %d", 1, len(b))
			}
			data = getInt(b[:1])

		case "jpeg", "png":
			data = &Picture{
				Ext:      contentType,
				MIMEType: "image/" + contentType,
				Data:     b,
			}
		}
		m.data[name] = data

		return nil
	})
}

// readCustomAtom reads a free-form "----" atom, which has three sub atoms:
// mean, name and one or more data atoms.  The mean determines whether the atom
// is a known custom tag; the name is used as the tag name.
func readCustomAtom(r io.ReadSeeker, start, end int64) (mean, name string, data []string, err error) {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return "", "", nil, err
	}
	err = walkBoxes(r, end, func(subName string, payloadStart, boxEnd int64) error {
		size := boxEnd - payloadStart
		if size < 4 {
			return fmt.Errorf("invalid encoding: expected at least %d bytes, got %d", 4, size)
		}
		b, err := readBytes(r, uint(size))
		if err != nil {
			return err
		}
		switch subName {
		case "mean":
			mean = string(b[4:])
		case "name":
			name = string(b[4:])
		case "data":
			data = append(data, string(b[4:]))
		}
		return nil
	})
	if err != nil {
		return "", "", nil, err
	}
	return mean, name, data, nil
}

// readBoxHeader reads an ISO BMFF box header from r.  remaining is the number
// of bytes left in the enclosing box (or file), used to resolve a zero size and
// to validate the declared size.  A box which claims to extend beyond its
// parent is reported as truncated, with its size clamped to remaining.
func readBoxHeader(r io.ReadSeeker, remaining int64) (name string, size, headerLen int64, err error) {
	b, err := readBytes(r, 8)
	if err != nil {
		return "", 0, 0, err
	}
	size32 := binary.BigEndian.Uint32(b[0:4])
	name = string(b[4:8])

	switch size32 {
	case 0:
		// The box extends to the end of the enclosing box/file.
		size = remaining
		headerLen = 8

	case 1:
		// A 64-bit size follows the type field.
		if remaining < 16 {
			return "", 0, 0, fmt.Errorf("invalid extended size for atom %q", name)
		}
		lb, err := readBytes(r, 8)
		if err != nil {
			return "", 0, 0, err
		}
		large := binary.BigEndian.Uint64(lb)
		if large < 16 || large > math.MaxInt64 {
			return "", 0, 0, fmt.Errorf("invalid extended size %d for atom %q", large, name)
		}
		size = int64(large)
		headerLen = 16

	default:
		size = int64(size32)
		headerLen = 8
	}

	if size < headerLen {
		return "", 0, 0, fmt.Errorf("invalid atom size %d for %q", size, name)
	}
	if size > remaining {
		// The box is truncated (or its size is corrupt): it can only
		// extend to the end of its parent, after which the walk stops.
		size = remaining
	}
	return name, size, headerLen, nil
}

// walkBoxes reads the sequence of ISO BMFF boxes from the current position
// until end, calling visit for each box.  visit is called with the box name,
// the absolute offset of the box payload and the absolute offset of the end of
// the box.  After visit returns, the reader is positioned at the end of the
// box, regardless of how much of it visit consumed.
func walkBoxes(r io.ReadSeeker, end int64, visit func(name string, payloadStart, boxEnd int64) error) error {
	for {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if pos >= end {
			return nil
		}
		if end-pos < 8 {
			// Trailing bytes do not form a valid box: ignore them.
			_, err := r.Seek(end, io.SeekStart)
			return err
		}

		name, size, headerLen, err := readBoxHeader(r, end-pos)
		if err != nil {
			return err
		}
		payloadStart := pos + headerLen
		boxEnd := pos + size

		if err := visit(name, payloadStart, boxEnd); err != nil {
			return err
		}

		if _, err := r.Seek(boxEnd, io.SeekStart); err != nil {
			return err
		}
	}
}

// readSeekerEnd returns the absolute end offset of r, leaving the current
// position unchanged.
func readSeekerEnd(r io.ReadSeeker) (int64, error) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := r.Seek(pos, io.SeekStart); err != nil {
		return 0, err
	}
	return end, nil
}

// mp4FileType determines the FileType of an MP4 file from its ftyp box (which
// identifies M4A, M4B and M4P files) and its stsd box (which identifies ALAC
// audio).  Any other MP4 file is returned as UnknownFileType.  ALAC takes
// precedence over the ftyp brand, since it describes the audio encoding.
//
// The current position of r is restored before returning.
func mp4FileType(r io.ReadSeeker) FileType {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return UnknownFileType
	}
	defer r.Seek(pos, io.SeekStart)

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return UnknownFileType
	}
	end, err := readSeekerEnd(r)
	if err != nil {
		return UnknownFileType
	}

	var (
		major      string
		compatible []string
		alac       bool
	)

	// Ignore traversal errors: use whatever was successfully read, since the
	// ftyp box (which is found first) is enough to identify most files.
	_ = walkBoxes(r, end, func(name string, payloadStart, boxEnd int64) error {
		switch name {
		case "ftyp":
			major, compatible, _ = readFileTypeBox(r, payloadStart, boxEnd)

		case "moov":
			if _, err := r.Seek(payloadStart, io.SeekStart); err != nil {
				return err
			}
			return walkBoxes(r, boxEnd, func(name string, payloadStart, boxEnd int64) error {
				return findALAC(r, name, payloadStart, boxEnd, 0, &alac)
			})
		}
		return nil
	})

	// stsd describes the audio encoding, so ALAC takes precedence over the
	// ftyp brand (ALAC files are usually wrapped in an M4A shell).
	if alac {
		return ALAC
	}
	// The major brand identifies the file type; fall back to the compatible
	// brands for files which declare their M4A/M4B/M4P nature there.
	if ft := fileTypeFromBrand(major); ft != UnknownFileType {
		return ft
	}
	for _, brand := range compatible {
		if ft := fileTypeFromBrand(brand); ft != UnknownFileType {
			return ft
		}
	}
	return UnknownFileType
}

// findALAC walks the boxes nested within moov looking for stsd boxes, and
// records whether any of them describes ALAC audio.
func findALAC(r io.ReadSeeker, name string, payloadStart, boxEnd int64, depth int, alac *bool) error {
	if depth > maxBoxDepth {
		return nil
	}
	switch name {
	case "trak", "mdia", "minf", "stbl":
		if _, err := r.Seek(payloadStart, io.SeekStart); err != nil {
			return err
		}
		return walkBoxes(r, boxEnd, func(name string, payloadStart, boxEnd int64) error {
			return findALAC(r, name, payloadStart, boxEnd, depth+1, alac)
		})

	case "stsd":
		// stsd is a full box followed by an entry count.
		if boxEnd-payloadStart < 8 {
			return nil
		}
		if _, err := r.Seek(payloadStart+8, io.SeekStart); err != nil {
			return err
		}
		return walkBoxes(r, boxEnd, func(name string, payloadStart, boxEnd int64) error {
			if name == "alac" {
				*alac = true
			}
			return nil
		})
	}
	return nil
}

// readFileTypeBox returns the major brand and compatible brands of an ftyp
// box.
func readFileTypeBox(r io.ReadSeeker, start, end int64) (major string, compatible []string, err error) {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return "", nil, err
	}
	// major_brand (4 bytes) + minor_version (4 bytes).
	if end-start < 8 {
		return "", nil, nil
	}
	b, err := readBytes(r, 8)
	if err != nil {
		return "", nil, err
	}
	major = string(b[0:4])

	// Cap the number of brands read from a malformed file.
	for p := start + 8; p+4 <= end && len(compatible) < 1024; p += 4 {
		b, err := readBytes(r, 4)
		if err != nil {
			return major, compatible, err
		}
		compatible = append(compatible, string(b))
	}
	return major, compatible, nil
}

// fileTypeFromBrand maps an ftyp brand to a FileType.
func fileTypeFromBrand(brand string) FileType {
	switch strings.TrimRight(brand, " \x00") {
	case "M4A":
		return M4A
	case "M4B":
		return M4B
	case "M4P":
		return M4P
	}
	return UnknownFileType
}

func (metadataMP4) Format() Format       { return MP4 }
func (m metadataMP4) FileType() FileType { return m.fileType }

func (m metadataMP4) Raw() map[string]interface{} { return m.data }

func (m metadataMP4) getString(n []string) string {
	for _, k := range n {
		if x, ok := m.data[k]; ok {
			return x.(string)
		}
	}
	return ""
}

func (m metadataMP4) getInt(n []string) int {
	for _, k := range n {
		if x, ok := m.data[k]; ok {
			return x.(int)
		}
	}
	return 0
}

func (m metadataMP4) Title() string {
	return m.getString(atoms.Name("title"))
}

func (m metadataMP4) Artist() string {
	return m.getString(atoms.Name("artist"))
}

func (m metadataMP4) Album() string {
	return m.getString(atoms.Name("album"))
}

func (m metadataMP4) AlbumArtist() string {
	return m.getString(atoms.Name("album_artist"))
}

func (m metadataMP4) Composer() string {
	return m.getString(atoms.Name("composer"))
}

func (m metadataMP4) Genre() string {
	return m.getString(atoms.Name("genre"))
}

func (m metadataMP4) Year() int {
	date := m.getString(atoms.Name("year"))
	if len(date) >= 4 {
		year, _ := strconv.Atoi(date[:4])
		return year
	}
	return 0
}

func (m metadataMP4) Track() (int, int) {
	x := m.getInt([]string{"trkn"})
	if n, ok := m.data["trkn_count"]; ok {
		return x, n.(int)
	}
	return x, 0
}

func (m metadataMP4) Disc() (int, int) {
	x := m.getInt([]string{"disk"})
	if n, ok := m.data["disk_count"]; ok {
		return x, n.(int)
	}
	return x, 0
}

func (m metadataMP4) Lyrics() string {
	t, ok := m.data["\xa9lyr"]
	if !ok {
		return ""
	}
	return t.(string)
}

func (m metadataMP4) Comment() string {
	t, ok := m.data["\xa9cmt"]
	if !ok {
		return ""
	}
	return t.(string)
}

func (m metadataMP4) Picture() *Picture {
	v, ok := m.data["covr"]
	if !ok {
		return nil
	}
	p, _ := v.(*Picture)
	return p
}
