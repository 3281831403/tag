// Copyright 2015, David Howden
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tag

import (
	"errors"
	"io"
)

// blockType is a type which represents an enumeration of valid FLAC blocks
type blockType byte

// FLAC block types.
const (
	// Stream Info Block           0
	// Padding Block               1
	// Application Block           2
	// Seektable Block             3
	// Cue Sheet Block             5
	vorbisCommentBlock blockType = 4
	pictureBlock       blockType = 6
)

// ReadFLACTags reads FLAC metadata from the io.ReadSeeker, returning the resulting
// metadata in a Metadata implementation, or non-nil error if there was a problem.
func ReadFLACTags(r io.ReadSeeker) (Metadata, error) {
	flac, err := readString(r, 4)
	if err != nil {
		return nil, err
	}
	if flac != "fLaC" {
		return nil, errors.New("expected 'fLaC'")
	}

	m := &metadataFLAC{
		newMetadataVorbis(),
	}

	for {
		last, err := m.readFLACMetadataBlock(r)
		if err != nil {
			return nil, err
		}

		if last {
			break
		}
	}
	return m, nil
}

type metadataFLAC struct {
	*metadataVorbis
}

func (m *metadataFLAC) readFLACMetadataBlock(r io.ReadSeeker) (last bool, err error) {
	blockHeader, err := readBytes(r, 1)
	if err != nil {
		return
	}

	if getBit(blockHeader[0], 7) {
		blockHeader[0] ^= (1 << 7)
		last = true
	}

	blockLen, err := readInt(r, 3)
	if err != nil {
		return
	}

	// Record where the block payload starts so the reader can always be
	// repositioned at the next block header, regardless of whether this
	// block's contents are malformed or shorter than the declared length.
	blockStart, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return
	}

	switch blockType(blockHeader[0]) {
	case vorbisCommentBlock:
		// A malformed comment block only loses this block; every other
		// block (and the tags it carries) must survive.
		_ = m.readVorbisComment(&io.LimitedReader{R: r, N: int64(blockLen)})

	case pictureBlock:
		// A bad picture is skipped rather than allowed to consume the
		// following blocks.
		_ = m.readPictureBlock(&io.LimitedReader{R: r, N: int64(blockLen)})
	}

	// Trust the block header's declared length rather than the number of
	// bytes the block parsers actually consumed.
	if _, seekErr := r.Seek(blockStart+int64(blockLen), io.SeekStart); seekErr != nil {
		err = seekErr
	}
	return
}

func (m *metadataFLAC) FileType() FileType {
	return FLAC
}
