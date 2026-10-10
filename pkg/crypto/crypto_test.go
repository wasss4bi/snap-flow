package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateKey(t *testing.T) {
	password := "test-password"
	key := GenerateKey(password)

	assert.Len(t, key, keySize)

	// Проверяем, что одинаковые пароли дают одинаковые ключи
	key2 := GenerateKey(password)
	assert.Equal(t, key, key2)

	// Проверяем, что разные пароли дают разные ключи
	key3 := GenerateKey("different-password")
	assert.NotEqual(t, key, key3)
}

func TestEncryptDecrypt(t *testing.T) {
	key := GenerateKey("test-password")
	originalData := []byte("This is test data for encryption")

	// Тест шифрования
	encryptedData, err := Encrypt(originalData, key)
	assert.NoError(t, err)
	assert.NotEmpty(t, encryptedData)
	assert.NotEqual(t, originalData, encryptedData)

	// Тест расшифровки
	decryptedData, err := Decrypt(encryptedData, key)
	assert.NoError(t, err)
	assert.Equal(t, originalData, decryptedData)
}

func TestEncryptDecryptLargeData(t *testing.T) {
	key := GenerateKey("test-password")

	// Создаем большой массив данных (1MB)
	originalData := bytes.Repeat([]byte("A"), 1024*1024)

	encryptedData, err := Encrypt(originalData, key)
	assert.NoError(t, err)
	assert.NotEmpty(t, encryptedData)

	decryptedData, err := Decrypt(encryptedData, key)
	assert.NoError(t, err)
	assert.Equal(t, originalData, decryptedData)
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1 := GenerateKey("password1")
	key2 := GenerateKey("password2")
	originalData := []byte("Secret data")

	encryptedData, err := Encrypt(originalData, key1)
	assert.NoError(t, err)

	// Попытка расшифровки с неправильным ключом
	_, err = Decrypt(encryptedData, key2)
	assert.Error(t, err)
}

func TestDecryptInvalidData(t *testing.T) {
	key := GenerateKey("test-password")

	// Тест с коротким ciphertext
	_, err := Decrypt([]byte("short"), key)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ciphertext too short")

	// Тест с поврежденными данными
	_, err = Decrypt([]byte("this is not encrypted data"), key)
	assert.Error(t, err)
}

func TestEncryptionUniqueness(t *testing.T) {
	key := GenerateKey("test-password")
	originalData := []byte("Same data")

	// Шифруем одни и те же данные дважды
	encrypted1, err := Encrypt(originalData, key)
	assert.NoError(t, err)

	encrypted2, err := Encrypt(originalData, key)
	assert.NoError(t, err)

	// Зашифрованные данные должны отличаться из-за разных nonce
	assert.NotEqual(t, encrypted1, encrypted2)

	// Но расшифровка должна давать одинаковый результат
	decrypted1, err := Decrypt(encrypted1, key)
	assert.NoError(t, err)

	decrypted2, err := Decrypt(encrypted2, key)
	assert.NoError(t, err)

	assert.Equal(t, decrypted1, decrypted2)
	assert.Equal(t, originalData, decrypted1)
}
