// Package auth — пароли и токены сессий.
//
// Токен — это случайные 32 байта в base64url; в базе лежит только sha256 от
// него, поэтому дамп БД не даёт ни войти, ни продлить чужую сессию.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword считает bcrypt-хеш пароля.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("хеширование пароля: %w", err)
	}
	return string(h), nil
}

// CheckPassword сверяет пароль с хешем.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewToken выдаёт новый токен сессии и его хеш для хранения.
func NewToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("генерация токена: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken — то, что реально лежит в таблице sessions.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashIP прячет IP за солью сервера: для рейт-лимита хватает сравнения
// хешей, а хранить сырые адреса посетителей незачем.
func HashIP(salt, ip string) string {
	sum := sha256.Sum256([]byte(salt + "|" + ip))
	return hex.EncodeToString(sum[:])
}
