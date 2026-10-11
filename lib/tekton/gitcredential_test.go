package tekton

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitCredentialHelperReadOnly drives real git through GitCredentialHelper
// over a file in a read-only directory, like the step's creds mount: `fill`
// must answer from the file, and `approve`/`reject` (the store/erase git sends
// after an authentication) must leave the file alone and print nothing — the
// plain store helper died on its lock there.
func TestGitCredentialHelperReadOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	creds := filepath.Join(home, "creds")
	if err := os.Mkdir(creds, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(creds, "git-credentials")
	const line = "https://x-access-token:tok@github.com\n"
	if err := os.WriteFile(file, []byte(line), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(creds, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(creds, 0o755) })

	credential := func(action, stdin string) (string, string) {
		t.Helper()
		// The empty credential.helper resets any helper from the caller's own git
		// config, so only ours runs.
		cmd := exec.Command("git", "-c", "credential.helper=", "-c", "credential.helper="+GitCredentialHelper(file), "credential", action)
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		cmd.Stdin = strings.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git credential %s: %v\n%s", action, err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	out, _ := credential("fill", "protocol=https\nhost=github.com\n\n")
	if !strings.Contains(out, "username=x-access-token\n") || !strings.Contains(out, "password=tok\n") {
		t.Errorf("fill should answer from the file, got:\n%s", out)
	}

	full := "protocol=https\nhost=github.com\nusername=x-access-token\npassword=tok\n\n"
	for _, action := range []string{"approve", "reject"} {
		if _, errOut := credential(action, full); errOut != "" {
			t.Errorf("%s should print nothing, got: %s", action, errOut)
		}
		if got, err := os.ReadFile(file); err != nil || string(got) != line {
			t.Errorf("%s must leave the file alone, got %q (%v)", action, got, err)
		}
	}
}
