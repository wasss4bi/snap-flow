package image

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/assert"
)

// createTestImage создает тестовое изображение в формате JPEG
func createTestImage(tb testing.TB, width, height int) []byte {
	tb.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Заполняем изображение простым градиентом
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8(x * 255 / width)  // #nosec G115 -- x is in [0, width), so the result is in [0, 254].
			g := uint8(y * 255 / height) // #nosec G115 -- y is in [0, height), so the result is in [0, 254].
			b := uint8(128)
			a := uint8(255)
			img.SetRGBA(x, y, color.RGBA{r, g, b, a})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateImage(t *testing.T) {
	tests := []struct {
		name        string
		imageData   []byte
		filename    string
		expectError bool
	}{
		{
			name:        "Valid JPEG image",
			imageData:   createTestImage(t, 100, 100),
			filename:    "test.jpg",
			expectError: false,
		},
		{
			name:        "Valid JPEG with .jpeg extension",
			imageData:   createTestImage(t, 100, 100),
			filename:    "test.jpeg",
			expectError: false,
		},
		{
			name:        "Unsupported format",
			imageData:   createTestImage(t, 100, 100),
			filename:    "test.bmp",
			expectError: true,
		},
		{
			name:        "Too large image",
			imageData:   make([]byte, MaxImageSize+1),
			filename:    "test.jpg",
			expectError: true,
		},
		{
			name:        "Invalid image data",
			imageData:   []byte("not an image"),
			filename:    "test.jpg",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImage(tt.imageData, tt.filename)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPreviouslyAcceptedLargeImageRemainsProcessable(t *testing.T) {
	data := createTestImage(t, 10, 10)
	data = append(data, make([]byte, MaxImageSize+1-len(data))...)
	if err := ValidateImage(data, "old.jpg"); err == nil {
		t.Fatal("new uploads must reject files above 5 MiB")
	}
	if _, _, err := ProcessImage(data, "old.jpg"); err != nil {
		t.Fatalf("previously accepted image: %v", err)
	}
}

func TestCreateThumbnail(t *testing.T) {
	// Создаем тестовое изображение 500x500
	originalImage := createTestImage(t, 500, 500)

	thumbnail, err := CreateThumbnail(originalImage)
	assert.NoError(t, err)
	assert.NotEmpty(t, thumbnail)

	// Проверяем, что миниатюра меньше оригинала
	assert.Less(t, len(thumbnail), len(originalImage))

	// Проверяем, что миниатюра является валидным JPEG
	_, err = jpeg.DecodeConfig(bytes.NewReader(thumbnail))
	assert.NoError(t, err)
}

func TestGetMimeType(t *testing.T) {
	tests := []struct {
		filename     string
		expectedMime string
	}{
		{"test.jpg", "image/jpeg"},
		{"test.jpeg", "image/jpeg"},
		{"test.JPG", "image/jpeg"},
		{"test.png", "image/png"},
		{"test.PNG", "image/png"},
		{"test.gif", "image/gif"},
		{"test.unknown", "application/octet-stream"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			mimeType := GetMimeType(tt.filename)
			assert.Equal(t, tt.expectedMime, mimeType)
		})
	}
}

func TestProcessImage(t *testing.T) {
	// Создаем валидное тестовое изображение
	originalImage := createTestImage(t, 400, 400)

	processedImage, thumbnail, err := ProcessImage(originalImage, "test.jpg")
	assert.NoError(t, err)
	assert.Equal(t, originalImage, processedImage) // Оригинал не должен изменяться
	assert.NotEmpty(t, thumbnail)
	assert.Less(t, len(thumbnail), len(originalImage)) // Миниатюра должна быть меньше

	// Тест с невалидными данными
	_, _, err = ProcessImage([]byte("invalid"), "test.jpg")
	assert.Error(t, err)
}

func TestReadImageFromReader(t *testing.T) {
	originalData := createTestImage(t, 100, 100)
	reader := bytes.NewReader(originalData)

	readData, err := ReadImageFromReader(reader)
	assert.NoError(t, err)
	assert.Equal(t, originalData, readData)
}
