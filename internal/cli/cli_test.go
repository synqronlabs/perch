package cli

import (
	"io"
	"os"
	"testing"
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
	if want := "perch " + Version + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}
