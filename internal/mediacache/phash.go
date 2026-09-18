package mediacache

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/bits"

	"github.com/disintegration/imaging"
)

const (
	phashSize = 32 // 标准化为 32×32
	phashBand = 8  // 取 DCT 左上 8×8
)

// PHashVersion identifies the perceptual-hash algorithm. Any change to the
// standardization or DCT selection must bump this version.
const PHashVersion = "dct32-v1"

// ErrImageDecode indicates the bytes cannot be decoded as a supported image.
var ErrImageDecode = errors.New("mediacache: decode image failed")

// dctCos[k][n] is the DCT-II cosine basis for N = phashSize.
//
//	cos(pi/N * (n + 0.5) * k)
var dctCos = func() [phashSize][phashSize]float64 {
	var table [phashSize][phashSize]float64
	for k := 0; k < phashSize; k++ {
		for n := 0; n < phashSize; n++ {
			table[k][n] = math.Cos(math.Pi / float64(phashSize) * (float64(n) + 0.5) * float64(k))
		}
	}
	return table
}()

// dct1D applies a one-dimensional DCT-II.
func dct1D(input []float64) []float64 {
	out := make([]float64, phashSize)
	for k := 0; k < phashSize; k++ {
		sum := 0.0
		for n := 0; n < phashSize; n++ {
			sum += input[n] * dctCos[k][n]
		}
		out[k] = sum
	}
	return out
}

// dctHash computes the 64-bit perceptual hash of an image already normalized
// to phashSize×phashSize: separable DCT, top-left 8×8, median threshold over
// the 63 non-DC coefficients.
func dctHash(img image.Image) uint64 {
	// Grayscale matrix.
	matrix := make([][]float64, phashSize)
	for r := 0; r < phashSize; r++ {
		row := make([]float64, phashSize)
		for c := 0; c < phashSize; c++ {
			gray := color.GrayModel.Convert(img.At(c, r)).(color.Gray)
			row[c] = float64(gray.Y)
		}
		matrix[r] = row
	}

	// DCT over rows.
	rows := make([][]float64, phashSize)
	for r := 0; r < phashSize; r++ {
		rows[r] = dct1D(matrix[r])
	}

	// DCT over columns.
	coeff := make([][]float64, phashSize)
	for r := range coeff {
		coeff[r] = make([]float64, phashSize)
	}
	for c := 0; c < phashSize; c++ {
		col := make([]float64, phashSize)
		for r := 0; r < phashSize; r++ {
			col[r] = rows[r][c]
		}
		out := dct1D(col)
		for r := 0; r < phashSize; r++ {
			coeff[r][c] = out[r]
		}
	}

	// Flatten the 8×8 low-frequency band.
	values := make([]float64, 0, phashBand*phashBand)
	for r := 0; r < phashBand; r++ {
		for c := 0; c < phashBand; c++ {
			values = append(values, coeff[r][c])
		}
	}

	// Mean threshold excluding the DC coefficient (index 0): the classic pHash
	// choice, stable under resizing and light recompression noise.
	var sum float64
	for i, v := range values {
		if i != 0 {
			sum += v
		}
	}
	mean := sum / float64(len(values)-1)

	var hash uint64
	for i, v := range values {
		if v > mean {
			hash |= 1 << (63 - i)
		}
	}
	return hash
}

// HammingDistance returns the number of differing bits between two perceptual
// hashes. Small distances indicate near-duplicate images.
func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

func decodeImage(data []byte) (image.Image, error) {
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrImageDecode, err)
	}
	return img, nil
}

// PHashFromBytes computes the standardized 64-bit perceptual hash of an image.
// GIF input hashes on its first frame (what imaging decodes). A decode failure
// returns an error wrapping ErrImageDecode and a zero hash.
func PHashFromBytes(data []byte) (uint64, error) {
	img, err := decodeImage(data)
	if err != nil {
		return 0, err
	}
	resized := imaging.Resize(img, phashSize, phashSize, imaging.Lanczos)
	return dctHash(resized), nil
}
