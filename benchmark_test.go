package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"snap-flow-go/pkg/crypto"
	imageprocessor "snap-flow-go/pkg/image"
	"testing"
)

// createBenchmarkImage создает тестовое изображение для бенчмарков
func createBenchmarkImage(tb testing.TB, width, height int) []byte {
	tb.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))

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

// BenchmarkImageProcessing тестирует производительность обработки изображений
func BenchmarkImageProcessing(b *testing.B) {
	imageData := createBenchmarkImage(b, 1024, 768) // Типичное разрешение

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := imageprocessor.ProcessImage(imageData, "test.jpg")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkThumbnailCreation тестирует производительность создания миниатюр
func BenchmarkThumbnailCreation(b *testing.B) {
	imageData := createBenchmarkImage(b, 2048, 1536) // Высокое разрешение

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := imageprocessor.CreateThumbnail(imageData)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncryption тестирует производительность шифрования
func BenchmarkEncryption(b *testing.B) {
	key := crypto.GenerateKey("test-password")
	data := createBenchmarkImage(b, 1024, 768)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := crypto.Encrypt(data, key)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecryption тестирует производительность расшифровки
func BenchmarkDecryption(b *testing.B) {
	key := crypto.GenerateKey("test-password")
	data := createBenchmarkImage(b, 1024, 768)

	encryptedData, err := crypto.Encrypt(data, key)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := crypto.Decrypt(encryptedData, key)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkImageValidation тестирует производительность валидации изображений
func BenchmarkImageValidation(b *testing.B) {
	imageData := createBenchmarkImage(b, 800, 600)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := imageprocessor.ValidateImage(imageData, "test.jpg")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConcurrentImageProcessing тестирует конкурентную обработку изображений
func BenchmarkConcurrentImageProcessing(b *testing.B) {
	imageData := createBenchmarkImage(b, 1024, 768)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _, err := imageprocessor.ProcessImage(imageData, "test.jpg")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
