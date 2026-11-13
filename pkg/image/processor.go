package image

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"path/filepath"
	"strings"

	"github.com/disintegration/imaging"
)

const (
	ThumbnailWidth  = 300
	ThumbnailHeight = 300
	MaxImageSize    = 10 * 1024 * 1024 // 10MB
)

// ValidateImage проверяет, является ли файл допустимым изображением
func ValidateImage(data []byte, filename string) error {
	if len(data) > MaxImageSize {
		return fmt.Errorf("image size exceeds maximum allowed size of %d bytes", MaxImageSize)
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		// Допустимые форматы
	default:
		return fmt.Errorf("unsupported image format: %s", ext)
	}

	// Проверяем, что данные действительно являются изображением
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid image data: %w", err)
	}

	if (format == "jpeg" && ext != ".jpg" && ext != ".jpeg") || (format == "png" && ext != ".png") || (format == "gif" && ext != ".gif") || (format != "jpeg" && format != "png" && format != "gif") {
		return fmt.Errorf("image format does not match extension")
	}
	if err == nil && (config.Width > 12000 || config.Height > 12000 || int64(config.Width)*int64(config.Height) > 40000000) {
		return fmt.Errorf("image dimensions exceed limit")
	}
	return nil
}

// CreateThumbnail создает миниатюру изображения
func CreateThumbnail(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Изменяем размер изображения с сохранением пропорций
	thumbnail := imaging.Fit(img, ThumbnailWidth, ThumbnailHeight, imaging.Lanczos)

	// Кодируем в JPEG для уменьшения размера
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 85})
	if err != nil {
		return nil, fmt.Errorf("failed to encode thumbnail: %w", err)
	}

	return buf.Bytes(), nil
}

// GetMimeType определяет MIME-тип изображения по расширению файла
func GetMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

// ProcessImage обрабатывает изображение (валидация + создание миниатюры)
func ProcessImage(data []byte, filename string) ([]byte, []byte, error) {
	// Валидация
	if err := ValidateImage(data, filename); err != nil {
		return nil, nil, err
	}

	// Создание миниатюры
	thumbnail, err := CreateThumbnail(data)
	if err != nil {
		return nil, nil, err
	}

	return data, thumbnail, nil
}

// ReadImageFromReader читает изображение из io.Reader
func ReadImageFromReader(reader io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := io.Copy(&buf, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}
	return buf.Bytes(), nil
}
