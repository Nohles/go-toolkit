package analyzer

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"

	"github.com/pkg/errors"
	"golang.org/x/image/webp"
)

const (
	// DimensionProbeInitialBytes is the number of leading bytes read on the
	// first dimension-probing attempt. Image headers are almost always within
	// this prefix, so a single ranged read usually suffices — including for
	// JPEGs carrying large EXIF thumbnails.
	DimensionProbeInitialBytes int64 = 64 * 1024

	// DimensionProbeMaxBytes caps how much of an image may be read while
	// probing its dimensions. Images whose headers exceed this budget are
	// skipped rather than fully read.
	DimensionProbeMaxBytes int64 = 1024 * 1024
)

// LeadingByteReader returns up to n leading bytes of an image. It may return
// fewer bytes than requested when the source is shorter; it must be safe to
// call repeatedly (each call restarts from the beginning of the image).
type LeadingByteReader func(n int64) ([]byte, error)

// ProbeImageDimensions determines an image's pixel dimensions from as few
// leading bytes as possible, without decoding any pixel data. When the first
// read is truncated mid-header, the reader is retried with a larger budget up
// to DimensionProbeMaxBytes.
//
// Formats handled: everything registered with the standard image package
// (GIF, JPEG, PNG) plus WebP, which is probed explicitly. AVIF and JXL are
// not supported and return an error, mirroring InspectImage.
func ProbeImageDimensions(read LeadingByteReader) (width uint, height uint, err error) {
	size := DimensionProbeInitialBytes
	for {
		data, readErr := read(size)
		if readErr != nil {
			return 0, 0, readErr
		}
		if len(data) == 0 {
			return 0, 0, errors.New("image is empty")
		}

		config, _, configErr := image.DecodeConfig(bytes.NewReader(data))
		if configErr == nil {
			return checkedDimensions(config)
		}

		if looksLikeWebP(data) {
			webpConfig, webpErr := webp.DecodeConfig(bytes.NewReader(data))
			if webpErr == nil {
				return checkedDimensions(webpConfig)
			}
			configErr = webpErr
		}

		if !errors.Is(configErr, io.ErrUnexpectedEOF) && !errors.Is(configErr, io.EOF) {
			return 0, 0, configErr
		}
		if size >= DimensionProbeMaxBytes {
			return 0, 0, fmt.Errorf("image header exceeds %d bytes", DimensionProbeMaxBytes)
		}
		size = min(size*2, DimensionProbeMaxBytes)
	}
}

// ProbeImageDimensionsInFS probes the dimensions of the bitmap at path within fsys.
func ProbeImageDimensionsInFS(fsys fs.FS, path string) (uint, uint, error) {
	width, height, err := ProbeImageDimensions(func(n int64) ([]byte, error) {
		file, err := fsys.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		data := make([]byte, n)
		read, err := io.ReadFull(file, data)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		return data[:read], nil
	})
	if err != nil {
		return 0, 0, errors.Wrapf(err, "failed probing dimensions of %s", path)
	}
	return width, height, nil
}

func checkedDimensions(config image.Config) (uint, uint, error) {
	if config.Width <= 0 || config.Height <= 0 {
		return 0, 0, errors.New("image has zero width or height")
	}
	return uint(config.Width), uint(config.Height), nil
}

func looksLikeWebP(data []byte) bool {
	return len(data) >= 15 &&
		bytes.Equal(data[0:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP"))
}
