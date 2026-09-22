package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrBadCredentials = errors.New("store: неверная почта или пароль")
	ErrNoSession      = errors.New("store: сессии нет или она истекла")
	ErrWeakPassword   = errors.New("store: пароль короче 8 символов")
)

const SessionTTL = 30 * 24 * time.Hour

type User struct {
	ID    uint64 `json:"id"`
	Email string `json:"email"`
}

// HashPassword — bcrypt с запасом по стоимости.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", ErrWeakPassword
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(h), err
}

// dummyHash сравнивается, когда пользователя нет: иначе по времени ответа
// можно было бы узнать, какие почты зарегистрированы. Считается при первом
// входе, а не при запуске: в демо (WebAssembly) вход не нужен вовсе.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("snag-dummy-password"), 12)
	return h
})

// CheckPassword сравнивает пароль с хешем; пустой хеш — «пользователя нет».
func CheckPassword(hash, password string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewSessionToken возвращает токен для cookie и его хеш для базы.
func NewSessionToken() (token string, hash []byte) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, HashToken(token)
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
