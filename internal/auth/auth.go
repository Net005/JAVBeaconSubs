package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const passwordIterations = 210_000

type Record struct {
	Username      string `json:"username"`
	Salt          string `json:"salt"`
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
	Iterations    int    `json:"iterations"`
}

type Store interface {
	LoadWebAuth() (Record, bool, error)
	SaveWebAuth(Record) error
}

type Manager struct {
	mu     sync.RWMutex
	store  Store
	record Record
	ready  bool
}

func New(store Store, seedUsername, seedPassword string) (*Manager, error) {
	m := &Manager{store: store}
	record, ok, err := store.LoadWebAuth()
	if err != nil {
		return nil, err
	}
	if ok {
		m.record, m.ready = record, true
		return m, nil
	}
	if strings.TrimSpace(seedUsername) != "" && seedPassword != "" {
		if err := m.Setup(seedUsername, seedPassword); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) Status() (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.record.Username, m.ready
}

func (m *Manager) Setup(username, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ready {
		return errors.New("web account is already configured")
	}
	record, err := makeRecord(username, password)
	if err != nil {
		return err
	}
	if err := m.store.SaveWebAuth(record); err != nil {
		return err
	}
	m.record, m.ready = record, true
	return nil
}

func (m *Manager) Authenticate(username, password string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.ready {
		return "", errors.New("web account setup is required")
	}
	if subtle.ConstantTimeCompare([]byte(username), []byte(m.record.Username)) != 1 {
		return "", errors.New("invalid username or password")
	}
	salt, err := base64.RawURLEncoding.DecodeString(m.record.Salt)
	if err != nil {
		return "", errors.New("invalid stored credentials")
	}
	want, err := base64.RawURLEncoding.DecodeString(m.record.PasswordHash)
	if err != nil {
		return "", errors.New("invalid stored credentials")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, m.record.Iterations, len(want))
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		return "", errors.New("invalid username or password")
	}
	return signSession(m.record, time.Now().UTC().Add(7*24*time.Hour))
}

func (m *Manager) Valid(token string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.ready || token == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	secret, err := base64.RawURLEncoding.DecodeString(m.record.SessionSecret)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 2 || subtle.ConstantTimeCompare([]byte(fields[0]), []byte(m.record.Username)) != 1 {
		return false
	}
	expires, err := strconv.ParseInt(fields[1], 10, 64)
	return err == nil && time.Now().Unix() < expires
}

func makeRecord(username, password string) (Record, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 {
		return Record{}, errors.New("username must be 3 to 64 characters")
	}
	if len(password) < 10 {
		return Record{}, errors.New("password must be at least 10 characters")
	}
	salt := make([]byte, 16)
	secret := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return Record{}, err
	}
	if _, err := rand.Read(secret); err != nil {
		return Record{}, err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return Record{}, err
	}
	return Record{Username: username, Salt: base64.RawURLEncoding.EncodeToString(salt), PasswordHash: base64.RawURLEncoding.EncodeToString(hash), SessionSecret: base64.RawURLEncoding.EncodeToString(secret), Iterations: passwordIterations}, nil
}

func signSession(record Record, expires time.Time) (string, error) {
	secret, err := base64.RawURLEncoding.DecodeString(record.SessionSecret)
	if err != nil {
		return "", fmt.Errorf("decode session secret: %w", err)
	}
	payload := []byte(record.Username + "|" + strconv.FormatInt(expires.Unix(), 10))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
