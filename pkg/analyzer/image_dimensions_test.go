package analyzer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	jpeg "image/jpeg"
	png "image/png"
	"io"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A test-only format whose headers only decode once enough bytes are provided,
// used to exercise the progressive read strategy without crafting real images.
var testProbeRequiredBytes = 200 * 1024

func init() {
	image.RegisterFormat("testprobe", "TESTPROBE",
		func(io.Reader) (image.Image, error) { return nil, fmt.Errorf("decoding unsupported") },
		decodeConfigTestProbe)
}

func decodeConfigTestProbe(r io.Reader) (image.Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return image.Config{}, err
	}
	if len(data) < testProbeRequiredBytes {
		return image.Config{}, io.ErrUnexpectedEOF
	}
	return image.Config{Width: 11, Height: 22}, nil
}

func testProbeReader() LeadingByteReader {
	data := make([]byte, 512*1024)
	copy(data, "TESTPROBE")
	return func(n int64) ([]byte, error) {
		size := int64(len(data))
		if n < size {
			size = n
		}
		return data[:size], nil
	}
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0, 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func jpegBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestProbeImageDimensionsPNG(t *testing.T) {
	width, height, err := ProbeImageDimensions(func(n int64) ([]byte, error) {
		data := pngBytes(t, 320, 640)
		if n < int64(len(data)) {
			data = data[:n]
		}
		return data, nil
	})
	require.NoError(t, err)
	assert.Equal(t, uint(320), width)
	assert.Equal(t, uint(640), height)
}

func TestProbeImageDimensionsJPEG(t *testing.T) {
	width, height, err := ProbeImageDimensions(func(n int64) ([]byte, error) {
		data := jpegBytes(t, 800, 14000)
		if n < int64(len(data)) {
			data = data[:n]
		}
		return data, nil
	})
	require.NoError(t, err)
	assert.Equal(t, uint(800), width)
	assert.Equal(t, uint(14000), height)
}

func TestProbeImageDimensionsGrowsReadOnTruncatedHeaders(t *testing.T) {
	read := testProbeReader()
	requestedSizes := make([]int64, 0, 4)
	wrapped := func(n int64) ([]byte, error) {
		requestedSizes = append(requestedSizes, n)
		return read(n)
	}

	width, height, err := ProbeImageDimensions(wrapped)
	require.NoError(t, err)
	assert.Equal(t, uint(11), width)
	assert.Equal(t, uint(22), height)

	// The initial budget is not enough for this format, so the probe must have
	// grown its request until the header decoded.
	require.GreaterOrEqual(t, len(requestedSizes), 2)
	assert.Equal(t, DimensionProbeInitialBytes, requestedSizes[0])
	assert.Greater(t, requestedSizes[len(requestedSizes)-1], DimensionProbeInitialBytes)
}

func TestProbeImageDimensionsGivesUpAtMaxBudget(t *testing.T) {
	testProbeRequiredBytes = 2 * 1024 * 1024
	t.Cleanup(func() { testProbeRequiredBytes = 200 * 1024 })

	read := testProbeReader()
	_, _, err := ProbeImageDimensions(read)
	require.ErrorContains(t, err, "image header exceeds")
}

func TestProbeImageDimensionsRejectsGarbage(t *testing.T) {
	_, _, err := ProbeImageDimensions(func(int64) ([]byte, error) {
		return []byte("this is not an image at all"), nil
	})
	require.Error(t, err)
}

func TestProbeImageDimensionsRejectsEmpty(t *testing.T) {
	_, _, err := ProbeImageDimensions(func(int64) ([]byte, error) {
		return nil, nil
	})
	require.ErrorContains(t, err, "empty")
}

func TestProbeImageDimensionsInFS(t *testing.T) {
	fsys := fstest.MapFS{
		"page.png": &fstest.MapFile{Data: pngBytes(t, 123, 456)},
	}
	width, height, err := ProbeImageDimensionsInFS(fsys, "page.png")
	require.NoError(t, err)
	assert.Equal(t, uint(123), width)
	assert.Equal(t, uint(456), height)

	_, _, err = ProbeImageDimensionsInFS(fsys, "missing.png")
	require.Error(t, err)
}
