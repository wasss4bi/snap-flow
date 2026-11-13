package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/logging"
	"snap-flow-go/internal/mailqueue"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB() *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logging.GORM{}})
	if err != nil {
		panic("failed to connect to test database: " + err.Error())
	}

	// Migrate the schema
	err = db.AutoMigrate(&models.User{}, &models.RevokedToken{}, &invite.Code{}, &limits.UserLimit{}, &confirmation.Code{}, &models.UserImage{}, &models.UploadOperation{}, &models.OutboxEvent{}, &maintenance.Deletion{}, &mailqueue.ConfirmationMail{})
	if err != nil {
		panic("failed to migrate test database: " + err.Error())
	}

	return db
}

type testMailer struct {
	token string
	fail  bool
}

func (m *testMailer) SendVerification(_ context.Context, _ string, token string) error {
	m.token = token
	if m.fail {
		return errors.New("mail unavailable")
	}
	return nil
}

func setupTestRouter() (*gin.Engine, *AuthHandler) {
	gin.SetMode(gin.TestMode)

	// Setup test database
	database.DB = setupTestDB()

	// Setup JWT manager
	jwtManager := auth.NewJWTManager("test-secret")
	authHandler := NewAuthHandler(jwtManager, &testMailer{})
	authHandler.SetMailSecret("test-secret")

	router := gin.New()

	return router, authHandler
}

func TestRegister(t *testing.T) {
	router, authHandler := setupTestRouter()
	router.POST("/api/register", authHandler.Register)
	_, code, _ := invite.Create(database.DB, time.Now().Add(time.Hour), 100000, time.Now())

	tests := []struct {
		name            string
		requestBody     models.RegisterRequest
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name: "Valid registration",
			requestBody: models.RegisterRequest{
				Name:       "TestUser",
				InviteCode: code,
				Email:      "test@example.com",
				Password:   "Password123!",
			},
			expectedStatus:  http.StatusCreated,
			expectedSuccess: true,
		},
		{
			name: "Invalid email",
			requestBody: models.RegisterRequest{
				Name:     "TestUser",
				Email:    "invalid-email",
				Password: "Password123!",
			},
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
		{
			name: "Short password",
			requestBody: models.RegisterRequest{
				Name:     "TestUser",
				Email:    "test2@example.com",
				Password: "123",
			},
			expectedStatus:  http.StatusBadRequest,
			expectedSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBody, _ := json.Marshal(tt.requestBody)
			req, _ := http.NewRequest("POST", "/api/register", bytes.NewBuffer(jsonBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response models.LoginResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedSuccess, response.Success)

			if tt.expectedSuccess {
				assert.Empty(t, response.Token)
			}
		})
	}
}

func TestLogin(t *testing.T) {
	router, authHandler := setupTestRouter()
	router.POST("/api/register", authHandler.Register)
	router.POST("/api/login", authHandler.Login)

	router.POST("/api/verify-email", authHandler.VerifyEmail)

	_, code, _ := invite.Create(database.DB, time.Now().Add(time.Hour), 100000, time.Now())
	// First, register a user
	registerReq := models.RegisterRequest{
		InviteCode: code,
		Name:       "TestUser",
		Email:      "test@example.com",
		Password:   "Password123!",
	}
	jsonBody, _ := json.Marshal(registerReq)
	req, _ := http.NewRequest("POST", "/api/register", bytes.NewBuffer(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration: %d", w.Code)
	}
	dispatchTestMail(t, authHandler)
	confirmation, _ := json.Marshal(map[string]string{"email": registerReq.Email, "code": authHandler.mailer.(*testMailer).token})
	verifyReq := httptest.NewRequest(http.MethodPost, "/api/verify-email", bytes.NewReader(confirmation))
	verifyReq.Header.Set("Content-Type", "application/json")
	verify := httptest.NewRecorder()
	router.ServeHTTP(verify, verifyReq)
	if verify.Code != http.StatusOK {
		t.Fatalf("verification: %d", verify.Code)
	}

	tests := []struct {
		name            string
		requestBody     models.LoginRequest
		expectedStatus  int
		expectedSuccess bool
	}{
		{
			name: "Valid login",
			requestBody: models.LoginRequest{
				Email:    "test@example.com",
				Password: "Password123!",
			},
			expectedStatus:  http.StatusOK,
			expectedSuccess: true,
		},
		{
			name: "Invalid password",
			requestBody: models.LoginRequest{
				Email:    "test@example.com",
				Password: "wrongpassword",
			},
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
		{
			name: "Non-existent user",
			requestBody: models.LoginRequest{
				Email:    "nonexistent@example.com",
				Password: "Password123!",
			},
			expectedStatus:  http.StatusUnauthorized,
			expectedSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonBody, _ := json.Marshal(tt.requestBody)
			req, _ := http.NewRequest("POST", "/api/login", bytes.NewBuffer(jsonBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response models.LoginResponse
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedSuccess, response.Success)

			if tt.expectedSuccess {
				assert.NotEmpty(t, response.Token)
			}
		})
	}
}

func dispatchTestMail(t *testing.T, h *AuthHandler) {
	t.Helper()
	found, err := mailqueue.Dispatch(context.Background(), database.DB, h.mailer, "test-secret", time.Now().UTC())
	if err != nil || !found {
		t.Fatalf("dispatch mail: %v", err)
	}
}
