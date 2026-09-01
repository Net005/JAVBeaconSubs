package auth

import "testing"

type memoryStore struct {
	record Record
	ready  bool
}

func (s *memoryStore) LoadWebAuth() (Record, bool, error) { return s.record, s.ready, nil }
func (s *memoryStore) SaveWebAuth(record Record) error {
	s.record, s.ready = record, true
	return nil
}

func TestSetupAuthenticateAndReload(t *testing.T) {
	store := &memoryStore{}
	manager, err := New(store, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Setup("subtitle-admin", "long-test-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate("subtitle-admin", "wrong-password"); err == nil {
		t.Fatal("wrong password was accepted")
	}
	token, err := manager.Authenticate("subtitle-admin", "long-test-password")
	if err != nil || !manager.Valid(token) {
		t.Fatalf("valid login failed: %v", err)
	}
	reloaded, err := New(store, "", "")
	if err != nil || !reloaded.Valid(token) {
		t.Fatalf("persisted session was rejected: %v", err)
	}
}
