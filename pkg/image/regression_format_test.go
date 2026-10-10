package image

import "testing"

func TestRejectImageFormatMismatchedExtension(t *testing.T) {
	if err := ValidateImage(createTestImage(t, 4, 4), "actual-jpeg.png"); err == nil {
		t.Fatal("JPEG accepted with PNG extension")
	}
}
