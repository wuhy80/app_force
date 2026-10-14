package agentbundle

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
)

func TestBundledAgentsMatchManifestAndRejectUnsupportedPlatform(t *testing.T) {
	platforms := Platforms()
	if len(platforms) == 0 {
		t.Skip("deployment payloads have not been generated, or this is an agent build")
	}
	if len(platforms) != 4 {
		t.Fatalf("expected four platform agents, got %d", len(platforms))
	}
	for _, platform := range platforms {
		t.Run(platform.OS+"/"+platform.Arch, func(t *testing.T) {
			reader, metadata, err := Open(platform.OS, platform.Arch)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			hash := sha256.New()
			header := make([]byte, 32)
			if _, err := io.ReadFull(reader, header); err != nil {
				t.Fatal(err)
			}
			if platform.OS == "linux" {
				machine := uint16(62)
				if platform.Arch == "arm64" {
					machine = 183
				}
				if string(header[:4]) != "\x7fELF" || binary.LittleEndian.Uint16(header[18:20]) != machine {
					t.Fatal("Linux agent architecture does not match manifest")
				}
			} else {
				cpu := uint32(0x01000007)
				if platform.Arch == "arm64" {
					cpu = 0x0100000c
				}
				if binary.LittleEndian.Uint32(header[:4]) != 0xfeedfacf || binary.LittleEndian.Uint32(header[4:8]) != cpu {
					t.Fatal("Mac agent architecture does not match manifest")
				}
			}
			_, _ = hash.Write(header)
			size, err := io.Copy(hash, reader)
			size += int64(len(header))
			if err != nil || size != metadata.Size || hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
				t.Fatalf("payload integrity check failed: %d %v", size, err)
			}
		})
	}
	if _, _, err := Open("windows", "arm"); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}
