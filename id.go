package tag

import (
	"fmt"
	"io"
)

// Identify identifies the format and file type of the data in the ReadSeeker.
func Identify(r io.ReadSeeker) (format Format, fileType FileType, err error) {
	b, err := readBytes(r, 11)
	if err != nil {
		return
	}

	_, err = r.Seek(-11, io.SeekCurrent)
	if err != nil {
		err = fmt.Errorf("could not seek back to original position: %v", err)
		return
	}

	switch {
	case string(b[0:4]) == "fLaC":
		return VORBIS, FLAC, nil

	case string(b[0:4]) == "OggS":
		return VORBIS, OGG, nil

	case string(b[4:8]) == "ftyp":
		// The ftyp box gives the file type for most M4A/M4B/M4P files, and
		// the stsd box identifies Apple Lossless (ALAC) audio.
		return MP4, mp4FileType(r), nil

	case string(b[0:3]) == "ID3":
		b := b[3:]
		switch uint(b[0]) {
		case 2:
			format = ID3v2_2
		case 3:
			format = ID3v2_3
		case 4:
			format = ID3v2_4
		case 0, 1:
			fallthrough
		default:
			err = fmt.Errorf("ID3 version: %v, expected: 2, 3 or 4", uint(b[0]))
			return
		}
		return format, MP3, nil
	}

	n, err := r.Seek(-128, io.SeekEnd)
	if err != nil {
		return
	}

	tag, err := readString(r, 3)
	if err != nil {
		return
	}

	_, err = r.Seek(-n, io.SeekCurrent)
	if err != nil {
		return
	}

	if tag != "TAG" {
		err = ErrNoTagsFound
		return
	}
	return ID3v1, MP3, nil
}
