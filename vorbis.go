// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func newMetadataVorbis() *metadataVorbis {
	return &metadataVorbis{
		c: make(map[string]string),
	}
}

type vorbisComment struct {
	key string
	val string
}

type metadataVorbis struct {
	c        map[string]string // single-value view of the vorbis comments
	comments []vorbisComment   // all comments in file order, including repeated keys
	p        *Picture
}

// readVorbisComment reads a Vorbis comment block from r. A single malformed
// comment (e.g. one without '=') is skipped instead of invalidating every
// other tag, and an unparseable embedded picture is skipped instead of
// failing the whole read.
func (m *metadataVorbis) readVorbisComment(r io.Reader) error {
	vendorLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	vendor, err := readString(r, uint(vendorLen))
	if err != nil {
		return err
	}
	m.setComment("vendor", vendor)

	commentsLen, err := readUint32LittleEndian(r)
	if err != nil {
		return err
	}

	for i := uint32(0); i < commentsLen; i++ {
		l, err := readUint32LittleEndian(r)
		if err != nil {
			return err
		}
		s, err := readString(r, uint(l))
		if err != nil {
			return err
		}
		k, v, err := parseComment(s)
		if err != nil {
			// Skip the malformed comment and keep processing the rest.
			continue
		}
		m.setComment(strings.ToLower(k), v)
	}

	// A picture embedded as a base64 Vorbis comment (FLAC convention). A
	// bad value must not invalidate any of the tags parsed above.
	if b64data, ok := m.c["metadata_block_picture"]; ok {
		data, err := base64.StdEncoding.DecodeString(b64data)
		if err == nil {
			m.readPictureBlock(&io.LimitedReader{R: bytes.NewReader(data), N: int64(len(data))})
		}
	}

	return nil
}

// setComment stores a comment while preserving every entry. The map keeps
// the first value for a key (single-value accessors and their types stay
// unchanged), while the ordered comment list retains all occurrences.
func (m *metadataVorbis) setComment(k, v string) {
	if _, ok := m.c[k]; !ok {
		m.c[k] = v
	}
	m.comments = append(m.comments, vorbisComment{key: k, val: v})
}

// readPictureBlock reads a FLAC PICTURE block (or the decoded payload of a
// METADATA_BLOCK_PICTURE comment) from lr, which must be limited to the
// declared bounds of the block. Every length field is validated against the
// remaining bytes before allocation so that an oversized declared length
// cannot trigger a huge allocation.
func (m *metadataVorbis) readPictureBlock(lr *io.LimitedReader) error {
	b, err := readInt(lr, 4)
	if err != nil {
		return err
	}
	pictureType, ok := pictureTypes[byte(b)]
	if !ok {
		return fmt.Errorf("invalid picture type: %v", b)
	}
	mimeLen, err := readUint(lr, 4)
	if err != nil {
		return err
	}
	if uint64(mimeLen) > uint64(lr.N) {
		return fmt.Errorf("picture MIME type length %d exceeds remaining block size %d", mimeLen, lr.N)
	}
	mime, err := readString(lr, mimeLen)
	if err != nil {
		return err
	}

	ext := ""
	switch mime {
	case "image/jpeg":
		ext = "jpg"
	case "image/png":
		ext = "png"
	case "image/gif":
		ext = "gif"
	}

	descLen, err := readUint(lr, 4)
	if err != nil {
		return err
	}
	if uint64(descLen) > uint64(lr.N) {
		return fmt.Errorf("picture description length %d exceeds remaining block size %d", descLen, lr.N)
	}
	desc, err := readString(lr, descLen)
	if err != nil {
		return err
	}

	// We skip width <32>, height <32>, colorDepth <32>, coloresUsed <32>
	if lr.N < 16 {
		return fmt.Errorf("picture block truncated before dimensions: %d bytes remain", lr.N)
	}
	if _, err = readInt(lr, 4); err != nil { // width
		return err
	}
	if _, err = readInt(lr, 4); err != nil { // height
		return err
	}
	if _, err = readInt(lr, 4); err != nil { // color depth
		return err
	}
	if _, err = readInt(lr, 4); err != nil { // colors used
		return err
	}

	dataLen, err := readInt(lr, 4)
	if err != nil {
		return err
	}
	if dataLen < 0 || int64(dataLen) > lr.N {
		return fmt.Errorf("picture data length %d exceeds remaining block size %d", dataLen, lr.N)
	}
	data := make([]byte, dataLen)
	if _, err = io.ReadFull(lr, data); err != nil {
		return err
	}

	m.p = &Picture{
		Ext:         ext,
		MIMEType:    mime,
		Type:        pictureType,
		Description: desc,
		Data:        data,
	}
	return nil
}

func parseComment(c string) (k, v string, err error) {
	kv := strings.SplitN(c, "=", 2)
	if len(kv) != 2 {
		err = errors.New("vorbis comment must contain '='")
		return
	}
	k = kv[0]
	v = kv[1]
	return
}

func (m *metadataVorbis) Format() Format {
	return VORBIS
}

func (m *metadataVorbis) Raw() map[string]interface{} {
	raw := make(map[string]interface{}, len(m.comments))
	for _, c := range m.comments {
		if v, ok := raw[c.key]; ok {
			switch vv := v.(type) {
			case string:
				raw[c.key] = []string{vv, c.val}
			case []string:
				raw[c.key] = append(vv, c.val)
			}
		} else {
			raw[c.key] = c.val
		}
	}
	return raw
}

func (m *metadataVorbis) Title() string {
	return m.c["title"]
}

func (m *metadataVorbis) Artist() string {
	// ARTIST
	// The artist generally considered responsible for the work. In popular music
	// this is usually the performing band or singer. For classical music it would
	// be the composer. For an audio book it would be the author of the original text.
	return m.c["artist"]
}

func (m *metadataVorbis) Album() string {
	return m.c["album"]
}

func (m *metadataVorbis) AlbumArtist() string {
	// This field isn't actually included in the standard, though
	// it is commonly assigned to albumartist.
	return m.c["albumartist"]
}

func (m *metadataVorbis) Composer() string {
	if m.c["composer"] != "" {
		return m.c["composer"]
	}
	// PERFORMER
	// The artist(s) who performed the work. In classical music this would be the
	// conductor, orchestra, soloists. In an audio book it would be the actor who
	// did the reading. In popular music this is typically the same as the ARTIST
	// and is omitted.
	if m.c["performer"] != "" {
		return m.c["performer"]
	}
	return m.c["artist"]
}

func (m *metadataVorbis) Genre() string {
	return m.c["genre"]
}

func (m *metadataVorbis) Year() int {
	var dateFormat string

	// The date need to follow the international standard https://en.wikipedia.org/wiki/ISO_8601
	// and obviously the VorbisComment standard https://wiki.xiph.org/VorbisComment#Date_and_time
	switch len(m.c["date"]) {
	case 0:
		// Fallback on year tag as some files use that.
		if len(m.c["year"]) != 0 {
			year, err := strconv.Atoi(m.c["year"])
			if err == nil {
				return year
			}
		}
		return 0
	case 4:
		dateFormat = "2006"
	case 7:
		dateFormat = "2006-01"
	case 10:
		dateFormat = "2006-01-02"
	}

	t, _ := time.Parse(dateFormat, m.c["date"])
	return t.Year()
}

// trackNumber splits a "x/y" style TRACKNUMBER (or DISCNUMBER) into its
// parts. A value without a slash is parsed as a single number and returns
// zero for the total.
func trackNumber(s string) (x, n int) {
	xn := strings.SplitN(s, "/", 2)
	x, _ = strconv.Atoi(strings.TrimSpace(xn[0]))
	if len(xn) == 2 {
		n, _ = strconv.Atoi(strings.TrimSpace(xn[1]))
	}
	return x, n
}

func (m *metadataVorbis) Track() (int, int) {
	// https://wiki.xiph.org/Field_names
	x, n := trackNumber(m.c["tracknumber"])
	// An explicit TRACKTOTAL always wins over the total embedded in a
	// "x/y" style TRACKNUMBER.
	if total, err := strconv.Atoi(m.c["tracktotal"]); err == nil {
		n = total
	}
	return x, n
}

func (m *metadataVorbis) Disc() (int, int) {
	// https://wiki.xiph.org/Field_names
	x, n := trackNumber(m.c["discnumber"])
	// An explicit DISCTOTAL always wins over the total embedded in a
	// "x/y" style DISCNUMBER.
	if total, err := strconv.Atoi(m.c["disctotal"]); err == nil {
		n = total
	}
	return x, n
}

func (m *metadataVorbis) Lyrics() string {
	return m.c["lyrics"]
}

func (m *metadataVorbis) Comment() string {
	if m.c["comment"] != "" {
		return m.c["comment"]
	}
	return m.c["description"]
}

func (m *metadataVorbis) Picture() *Picture {
	return m.p
}
