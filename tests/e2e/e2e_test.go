//go:build integration

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"snap-flow-go/internal/config"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/pkg/crypto"
	imageprocessor "snap-flow-go/pkg/image"

	"github.com/google/uuid"
)

func TestUploadReadAndDelete(t *testing.T) {
	if os.Getenv("RUN_E2E") != "1" {
		t.Skip("run with the isolated Compose smoke service")
	}
	cfg := config.Load()
	if cfg.Storage.Driver != "local" {
		t.Fatal("smoke test requires the local backend")
	}
	if err := database.Connect(cfg); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	baseURL := os.Getenv("E2E_BASE_URL")
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(method, path, token, contentType string, body io.Reader) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, baseURL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	register := func() (models.User, string, string) {
		t.Helper()
		email := uuid.NewString() + "@example.test"
		_, inviteCode, err := invite.Create(db, time.Now().Add(time.Hour), 30*1024*1024, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(models.RegisterRequest{Name: "SmokeUser", Email: email, Password: "Local-test-password123!", InviteCode: inviteCode})
		if err != nil {
			t.Fatal(err)
		}
		status, data := request(http.MethodPost, "/api/register", "", "application/json", bytes.NewReader(body))
		if status != http.StatusCreated {
			t.Fatalf("registration: HTTP %d", status)
		}
		var response models.LoginResponse
		if err := json.Unmarshal(data, &response); err != nil || !response.Success || response.Token != "" {
			t.Fatalf("invalid registration response: %v", err)
		}
		mailURL := "http://mailpit:8025/api/v1/search?query=" + url.QueryEscape("to:"+email)
		var search struct {
			Messages []struct {
				ID string `json:"ID"`
			} `json:"messages"`
		}
		for attempt := 0; attempt < 50; attempt++ {
			mailResponse, err := client.Get(mailURL)
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(mailResponse.Body).Decode(&search)
			mailResponse.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if len(search.Messages) > 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(search.Messages) == 0 {
			t.Fatal("verification email missing")
		}
		messageResponse, err := client.Get("http://mailpit:8025/api/v1/message/" + search.Messages[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		var message struct {
			Text string `json:"Text"`
		}
		if err := json.NewDecoder(messageResponse.Body).Decode(&message); err != nil {
			messageResponse.Body.Close()
			t.Fatal(err)
		}
		messageResponse.Body.Close()
		matches := regexp.MustCompile(`confirmation code: ([0-9]{6})`).FindStringSubmatch(message.Text)
		if len(matches) != 2 {
			t.Fatal("verification token missing from email")
		}
		confirm, _ := json.Marshal(map[string]string{"email": email, "code": matches[1]})
		status, _ = request(http.MethodPost, "/api/verify-email", "", "application/json", bytes.NewReader(confirm))
		if status != http.StatusOK {
			t.Fatalf("confirmation: HTTP %d", status)
		}
		var user models.User
		if err := db.Where("email = ?", email).First(&user).Error; err != nil {
			t.Fatalf("registered user/API token missing: %v", err)
		}
		t.Cleanup(func() {
			db.Unscoped().Where("user_id = ?", user.ID).Delete(&models.UserImage{})
			db.Where("user_id = ?", user.ID).Delete(&confirmation.Code{})
			db.Where("user_id = ?", user.ID).Delete(&limits.UserLimit{})
			db.Unscoped().Delete(&models.User{}, user.ID)

		})
		loginBody, _ := json.Marshal(models.LoginRequest{Email: email, Password: "Local-test-password123!"})
		status, data = request(http.MethodPost, "/api/login", "", "application/json", bytes.NewReader(loginBody))
		if status != http.StatusOK {
			t.Fatalf("login: HTTP %d", status)
		}
		if err := json.Unmarshal(data, &response); err != nil || response.Token == "" {
			t.Fatal("login did not return JWT")
		}
		return user, response.Token, response.Token
	}
	owner, jwt, apiToken := register()
	_, otherJWT, _ := register()

	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	var originals [][]byte
	for _, format := range []string{"jpg", "png", "gif"} {
		img := image.NewRGBA(image.Rect(0, 0, 420, 240))
		for y := 0; y < 240; y++ {
			for x := 0; x < 420; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y), B: 100, A: 255})
			}
		}
		var encoded bytes.Buffer
		var err error
		switch format {
		case "jpg":
			err = jpeg.Encode(&encoded, img, nil)
		case "png":
			err = png.Encode(&encoded, img)
		case "gif":
			err = gif.Encode(&encoded, img, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		if format == "jpg" {
			// JPEG decoders ignore trailing bytes after EOI. This fixture checks
			// the full accepted 5 MiB size through encrypted staging and RabbitMQ.
			if _, err := encoded.Write(make([]byte, imageprocessor.MaxImageSize-encoded.Len())); err != nil {
				t.Fatal(err)
			}
		}
		part, err := writer.CreateFormFile("images", "fixture."+format)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(encoded.Bytes()); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, append([]byte(nil), encoded.Bytes()...))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, data := request(http.MethodPost, "/api/upload-images", apiToken, writer.FormDataContentType(), &form)
	if status != http.StatusAccepted {
		t.Fatalf("upload: HTTP %d", status)
	}
	var upload models.ImageUploadResponse
	if err := json.Unmarshal(data, &upload); err != nil || !upload.Success || len(upload.Files) != 3 {
		t.Fatalf("not all images were queued: count=%d error=%v", len(upload.Files), err)
	}
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", label)
	}
	var badBody bytes.Buffer
	badForm := multipart.NewWriter(&badBody)
	badFile, err := badForm.CreateFormFile("images", "broken.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badFile.Write(originals[1][:33]); err != nil {
		t.Fatal(err)
	}
	if err = badForm.Close(); err != nil {
		t.Fatal(err)
	}
	status, data = request(http.MethodPost, "/api/upload-images", jwt, badForm.FormDataContentType(), &badBody)
	var badUpload models.ImageUploadResponse
	if status != http.StatusAccepted || json.Unmarshal(data, &badUpload) != nil || len(badUpload.Files) != 1 {
		t.Fatalf("truncated image not accepted for asynchronous validation: %d", status)
	}
	wait("failed upload status", func() bool {
		status, body := request(http.MethodGet, "/api/uploads/"+badUpload.Files[0], jwt, "", nil)
		var result struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}
		return status == http.StatusOK && json.Unmarshal(body, &result) == nil && result.Status == "failed" && result.Reason != ""
	})
	wait("worker metadata", func() bool {
		var count int64
		return db.Model(&models.UserImage{}).Where("user_id = ?", owner.ID).Count(&count).Error == nil && count == 3
	})
	status, data = request(http.MethodGet, "/api/get-images-count", apiToken, "", nil)
	var countResponse models.ImageCountResponse
	if err := json.Unmarshal(data, &countResponse); err != nil || status != http.StatusOK || countResponse.ImagesCount != 3 {
		t.Fatalf("HTTP image count mismatch: %v", err)
	}
	var images []models.UserImage
	if err := db.Where("user_id = ?", owner.ID).Find(&images).Error; err != nil || len(images) != 3 {
		t.Fatalf("metadata mismatch: %v", err)
	}
	status, data = request(http.MethodGet, "/api/images?page=1&limit=2", jwt, "", nil)
	var list struct {
		Images []struct {
			ID uint `json:"id"`
		} `json:"images"`
		Total int `json:"total"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &list) != nil || list.Total != 3 || len(list.Images) != 2 {
		t.Fatal("paginated photo list failed")
	}
	for _, filename := range upload.Files {
		status, data = request(http.MethodGet, "/api/uploads/"+filename, jwt, "", nil)
		var uploadState struct {
			Status string `json:"status"`
		}
		if status != http.StatusOK || json.Unmarshal(data, &uploadState) != nil || uploadState.Status != "ready" {
			t.Fatalf("upload status not ready: %d", status)
		}
	}
	for i, filename := range upload.Files {
		status, plain := request(http.MethodGet, "/images/"+filename, jwt, "", nil)
		if status != http.StatusOK || !bytes.Equal(plain, originals[i]) {
			t.Fatalf("original %d did not round trip through HTTP", i)
		}
		status, thumb := request(http.MethodGet, "/thumbnails/"+filename, jwt, "", nil)
		if status != http.StatusOK {
			t.Fatalf("thumbnail %d: HTTP %d", i, status)
		}
		decoded, err := jpeg.DecodeConfig(bytes.NewReader(thumb))
		if err != nil || decoded.Width > 300 || decoded.Height > 300 || decoded.Width == 0 || decoded.Height == 0 {
			t.Fatalf("invalid thumbnail %d: %v", i, err)
		}
		for _, item := range []struct {
			prefix string
			plain  []byte
		}{
			{"uploads", originals[i]}, {"thumbnails", thumb},
		} {
			raw, err := os.ReadFile(filepath.Join(cfg.Storage.LocalPath, "private", item.prefix, filename))
			if err != nil || bytes.Equal(raw, item.plain) {
				t.Fatalf("missing/encrypted-at-rest check failed: %v", err)
			}
			decrypted, err := crypto.Decrypt(raw, crypto.GenerateKey(cfg.Storage.LocalKey))
			if err != nil || !bytes.Equal(decrypted, item.plain) {
				t.Fatalf("disk ciphertext does not decrypt to HTTP content: %v", err)
			}
		}
		for _, path := range []string{"/images/", "/thumbnails/"} {
			status, _ := request(http.MethodGet, path+filename, otherJWT, "", nil)
			if status != http.StatusNotFound {
				t.Fatalf("other owner could access %s: HTTP %d", path, status)
			}
			status, _ = request(http.MethodGet, path+filename, "", "", nil)
			if status != http.StatusUnauthorized {
				t.Fatalf("anonymous access: HTTP %d", status)
			}
		}
	}
	status, _ = request(http.MethodDelete, fmt.Sprintf("/delete-image/%d", images[0].ID), otherJWT, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("other owner could delete image: HTTP %d", status)
	}
	status, _ = request(http.MethodDelete, fmt.Sprintf("/delete-image/%d", images[0].ID), jwt, "", nil)
	if status != http.StatusOK {
		t.Fatalf("single delete: HTTP %d", status)
	}
	status, _ = request(http.MethodDelete, "/api/delete-images", apiToken, "", nil)
	if status != http.StatusOK {
		t.Fatalf("batch delete: HTTP %d", status)
	}
	wait("encrypted objects to be removed by worker", func() bool {
		for _, filename := range upload.Files {
			for _, dir := range []string{"uploads", "thumbnails"} {
				if _, err := os.Stat(filepath.Join(cfg.Storage.LocalPath, "private", dir, filename)); !os.IsNotExist(err) {
					return false
				}
			}
		}
		return true
	})
	var count int64
	if err := db.Model(&models.UserImage{}).Where("user_id = ?", owner.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("active metadata remains after deletion: %v", err)
	}
	for _, filename := range upload.Files {
		status, _ := request(http.MethodGet, "/images/"+filename, jwt, "", nil)
		if status != http.StatusNotFound {
			t.Fatalf("deleted image readable: HTTP %d", status)
		}
	}
	t.Log("3 JPEG/PNG/GIF originals (including 5 MiB) and 3 previews: HTTP → RabbitMQ → worker → PostgreSQL + encrypted disk, owner reads/access checks and asynchronous deletion passed")
}
