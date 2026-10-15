package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestNativeHelperProcess(t *testing.T) {
	result := os.Getenv("APPFORCE_TEST_NATIVE_RESULT")
	if result == "" {
		return
	}
	var err error
	switch result {
	case "missing":
		err = fmt.Errorf("managed CLI: %w", ErrNoCredential)
	case "failure":
		// Identical text is not sufficient: only the explicit missing status
		// permits account application to overwrite an absent credential.
		err = errors.New(ErrNoCredential.Error())
	case "success":
		err = json.NewEncoder(os.Stdout).Encode(testCredential())
	default:
		err = errors.New("unexpected helper test result")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "AppForce account: "+err.Error())
	}
	os.Exit(NativeHelperExitCode(err))
}

func TestNativeHelperPreservesMissingCredentialAcrossProcess(t *testing.T) {
	for _, result := range []string{"missing", "failure", "success"} {
		t.Run(result, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeHelperProcess$")
			cmd.Env = append(os.Environ(), "APPFORCE_TEST_NATIVE_RESULT="+result)
			credential, err := runNativeCommand(cmd, "read")
			switch result {
			case "missing":
				if !errors.Is(err, ErrNoCredential) {
					t.Fatalf("missing credential lost across helper boundary: %v", err)
				}
			case "failure":
				if err == nil || errors.Is(err, ErrNoCredential) {
					t.Fatalf("read failure treated as an absent credential: %v", err)
				}
			case "success":
				if err != nil || credential.Token.AccessToken != testCredential().Token.AccessToken {
					t.Fatalf("helper credential read failed: %v", err)
				}
			}
		})
	}
}
