//go:build !agent

package agentbundle

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"io"
	"path"
)

//go:embed payloads/*
var payloads embed.FS

func Platforms() []Platform {
	data, err := payloads.ReadFile("payloads/manifest.json")
	if err != nil {
		return []Platform{}
	}
	var platforms []Platform
	if json.Unmarshal(data, &platforms) != nil {
		return []Platform{}
	}
	return platforms
}

func openPayload(name string) (io.ReadCloser, error) {
	file, err := payloads.Open("payloads/" + path.Base(name))
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &payloadReader{Reader: reader, file: file}, nil
}

type payloadReader struct {
	*gzip.Reader
	file io.Closer
}

func (r *payloadReader) Close() error { _ = r.Reader.Close(); return r.file.Close() }
