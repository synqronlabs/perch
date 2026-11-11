package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v3"
)

func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	outReader, outWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outWriter, errWriter
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
		_ = outReader.Close()
		_ = errReader.Close()
	}()
	fn()
	_ = outWriter.Close()
	_ = errWriter.Close()
	out, err := io.ReadAll(outReader)
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := io.ReadAll(errReader)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(errOut)
}

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"no command", nil, 2},
		{"unknown", []string{"bogus"}, 2},
		{"version flag", []string{"--version"}, 0},
		{"single dash version", []string{"-version"}, 0},
		{"version command", []string{"version"}, 0},
		{"account usage", []string{"account"}, 1},
		{"alias usage", []string{"alias"}, 1},
		{"mailbox usage", []string{"mailbox"}, 1},
		{"mail usage", []string{"mail"}, 1},
		{"outbound usage", []string{"outbound"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			_, _ = captureOutput(t, func() { code = Run(tc.args) })
			if code != tc.code {
				t.Fatalf("Run(%v) = %d, want %d", tc.args, code, tc.code)
			}
		})
	}
}

func TestVersionOutput(t *testing.T) {
	var code int
	out, _ := captureOutput(t, func() { code = Run([]string{"version"}) })
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if want := "perch " + version() + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestUnknownMailCommandSuggestsSearch(t *testing.T) {
	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{"mail", "serch", "-q", "all"})
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		`Did you mean "search"?`,
		`unknown command "serch"; did you mean "search"?`,
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr does not contain %q:\n%s", want, errOut)
		}
	}
}

func TestUnknownRootCommandSuggestsMail(t *testing.T) {
	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{"mial", "list"})
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut, `did you mean "mail"?`) {
		t.Fatalf("stderr does not suggest mail:\n%s", errOut)
	}
}

func TestSuggestSubcommand(t *testing.T) {
	commands := []*cli.Command{
		{Name: "list", Aliases: []string{"ls"}},
		{Name: "search", Aliases: []string{"find"}},
		{Name: "fetch"},
		{Name: "send"},
		{Name: "caps"},
	}
	tests := []struct {
		input string
		want  string
	}{
		{"serch", "search"},
		{"lsit", "list"},
		{"lst", "list"},
		{"fnd", "search"},
		{"bogus", ""},
	}
	for _, tc := range tests {
		if got := suggestSubcommand(commands, tc.input); got != tc.want {
			t.Errorf("suggestSubcommand(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSetupDryRunDoesNotRequireAuthorization(t *testing.T) {
	t.Setenv("MXRAVEN_API_TOKEN", "")
	t.Setenv("MXRAVEN_CLIENT_ID", "")
	dir := t.TempDir()
	var code int
	out, _ := captureOutput(t, func() {
		code = Run([]string{
			"setup", "--workspace", "demo", "--domain", "example.com",
			"--dry-run", "--data-dir", dir,
		})
	})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out, "would create or reuse sending domain example.com") {
		t.Fatalf("output = %q", out)
	}
}

func TestSetupWithoutTokenRequiresClientID(t *testing.T) {
	t.Setenv("MXRAVEN_API_TOKEN", "")
	t.Setenv("MXRAVEN_CLIENT_ID", "")
	dir := t.TempDir()
	var code int
	_, errOut := captureOutput(t, func() {
		code = Run([]string{
			"setup", "--workspace", "demo", "--domain", "example.com",
			"--data-dir", dir, "--client-id", "",
		})
	})
	if code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(errOut, "no client ID configured") {
		t.Fatalf("stderr = %q", errOut)
	}
}
