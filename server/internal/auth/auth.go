package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

const (
	passwordAlgorithm  = "pbkdf2-sha256"
	passwordIterations = 210_000
	passwordSaltSize   = 16
	passwordKeySize    = 32
	maxHashIterations  = 10_000_000
)

// HashSecret 计算密钥的 SHA-256 标识。
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VerifySecret 常量时间校验密钥哈希。
func VerifySecret(secret, hash string) bool {
	actual := []byte(HashSecret(secret))
	expected := []byte(hash)
	return len(actual) == len(expected) && subtle.ConstantTimeCompare(actual, expected) == 1
}

// HashPassword 使用 PBKDF2 生成带盐密码哈希。
func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2Key([]byte(password), salt, passwordIterations, passwordKeySize, sha256.New)
	return fmt.Sprintf("%s$%d$%s$%s", passwordAlgorithm, passwordIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验 PBKDF2 编码密码。
func VerifyPassword(password, encoded string) (bool, error) {
	if strings.HasPrefix(encoded, "sha256:") {
		return VerifySecret(password, encoded), nil
	}
	iterations, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual := pbkdf2Key([]byte(password), salt, iterations, len(expected), sha256.New)
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

// PasswordNeedsRehash 判断密码哈希参数是否需要升级。
func PasswordNeedsRehash(encoded string) bool {
	iterations, salt, key, err := parsePasswordHash(encoded)
	return err != nil || iterations != passwordIterations || len(salt) != passwordSaltSize || len(key) != passwordKeySize
}

func parsePasswordHash(encoded string) (int, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordAlgorithm {
		return 0, nil, nil, errors.New("invalid password hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > maxHashIterations {
		return 0, nil, nil, errors.New("invalid password hash iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return 0, nil, nil, errors.New("invalid password hash salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(key) == 0 {
		return 0, nil, nil, errors.New("invalid password hash key")
	}
	return iterations, salt, key, nil
}

func pbkdf2Key(password, salt []byte, iterations, keyLength int, newHash func() hash.Hash) []byte {
	hashLength := newHash().Size()
	blocks := (keyLength + hashLength - 1) / hashLength
	derived := make([]byte, 0, blocks*hashLength)
	blockBuffer := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		binary.BigEndian.PutUint32(blockBuffer, uint32(block))
		mac := hmac.New(newHash, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write(blockBuffer)
		u := mac.Sum(nil)
		result := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(newHash, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range result {
				result[j] ^= u[j]
			}
		}
		derived = append(derived, result...)
	}
	return derived[:keyLength]
}
