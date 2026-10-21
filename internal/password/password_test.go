package password

import "testing"

func TestHashVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(hash, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Verify() = false, want true")
	}
	ok, err = Verify(hash, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Verify() = true for wrong password")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	if _, err := Verify("not-a-hash", "x"); err == nil {
		t.Fatal("Verify() error = nil for malformed hash")
	}
}
