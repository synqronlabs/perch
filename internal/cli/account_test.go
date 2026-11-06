package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/synqronlabs/perch/internal/config"
	"github.com/synqronlabs/perch/internal/password"
	"github.com/synqronlabs/perch/internal/store"
)

func testAccountConfig(t *testing.T) (config.Config, string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Hostname = "localhost"
	path := filepath.Join(cfg.DataDir, "perch.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func createTestAccount(t *testing.T, cfg config.Config, address string) {
	t.Helper()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	hash, err := password.Hash("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), address, "", hash); err != nil {
		t.Fatal(err)
	}
}

func TestAccountDelete(t *testing.T) {
	cfg, path := testAccountConfig(t)
	createTestAccount(t, cfg, "delete@example.com")

	var code int
	out, _ := captureOutput(t, func() {
		code = Run([]string{
			"account", "delete", "--config", path,
			"--email", "delete@example.com", "--yes",
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out, "deleted delete@example.com") {
		t.Fatalf("output = %q", out)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.AccountByAddress(context.Background(), "delete@example.com"); !errors.Is(err, store.ErrNoRecipient) {
		t.Fatalf("account after delete = %v", err)
	}
}

func TestAccountDeleteRequiresConfirmationWithoutTerminal(t *testing.T) {
	cfg, path := testAccountConfig(t)
	createTestAccount(t, cfg, "delete@example.com")

	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"account", "delete", "--config", path,
			"--email", "delete@example.com",
		})
	})
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(errOut, "use --yes") {
		t.Fatalf("stderr = %q", errOut)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.AccountByAddress(context.Background(), "delete@example.com"); err != nil {
		t.Fatalf("account after cancelled delete = %v", err)
	}
}
