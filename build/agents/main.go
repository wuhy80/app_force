// Command agents builds self-contained remote payloads before building the management application.
package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"appforce/internal/agentbundle"
)

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build() error {
	dir := filepath.Join("internal", "agentbundle", "payloads")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "appforce-agents-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	platforms := []agentbundle.Platform{}
	for _, goos := range []string{"linux", "darwin"} {
		for _, arch := range []string{"amd64", "arm64"} {
			fmt.Printf("构建远程 Agent: %s/%s\n", goos, arch)
			binary := filepath.Join(work, "appforce-"+goos+"-"+arch)
			cmd := exec.Command("go", "build", "-trimpath", "-tags", "agent,with_quic,with_utls,with_gvisor", "-ldflags", "-s -w", "-o", binary, "./cmd/appforce")
			env := []string{}
			for _, entry := range os.Environ() {
				if !hasEnvKey(entry, "GOOS") && !hasEnvKey(entry, "GOARCH") && !hasEnvKey(entry, "CGO_ENABLED") {
					env = append(env, entry)
				}
			}
			cmd.Env = append(env, "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return err
			}
			input, err := os.Open(binary)
			if err != nil {
				return err
			}
			name := "appforce-" + goos + "-" + arch + ".gz"
			output, err := os.Create(filepath.Join(dir, name+".tmp"))
			if err != nil {
				_ = input.Close()
				return err
			}
			compressed := gzip.NewWriter(output)
			hash := sha256.New()
			size, copyErr := io.Copy(io.MultiWriter(compressed, hash), input)
			_ = input.Close()
			zipErr, closeErr := compressed.Close(), output.Close()
			if copyErr != nil {
				return copyErr
			}
			if zipErr != nil {
				return zipErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Rename(filepath.Join(dir, name+".tmp"), filepath.Join(dir, name)); err != nil {
				return err
			}
			platforms = append(platforms, agentbundle.Platform{OS: goos, Arch: arch, File: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size})
		}
	}
	data, _ := json.MarshalIndent(platforms, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json.tmp"), data, 0644); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, "manifest.json.tmp"), filepath.Join(dir, "manifest.json"))
}

func hasEnvKey(entry, key string) bool { return len(entry) > len(key) && entry[:len(key)+1] == key+"=" }
