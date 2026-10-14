//go:build agent

package agentbundle

import (
	"fmt"
	"io"
)

// Uploaded agents do not recursively contain other platform binaries.
func Platforms() []Platform                     { return []Platform{} }
func openPayload(string) (io.ReadCloser, error) { return nil, fmt.Errorf("Agent 不包含部署包") }
