package identity

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	hasher := passwordHasher{}
	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	valid, err := hasher.Verify(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !valid {
		t.Fatal("correct password did not verify")
	}
	valid, err = hasher.Verify(encoded, "incorrect horse battery staple")
	if err != nil {
		t.Fatalf("Verify() wrong password error = %v", err)
	}
	if valid {
		t.Fatal("wrong password verified")
	}
}

func TestPasswordHashRejectsUnsafeStoredParameters(t *testing.T) {
	hasher := passwordHasher{}
	_, err := hasher.Verify("$argon2id$v=19$m=9999999,t=3,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U", "password")
	if err == nil {
		t.Fatal("Verify() accepted excessive memory parameters")
	}
}
