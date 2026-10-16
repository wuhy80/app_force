//go:build !windows

package wfp

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

func applicationID(string) (string, error) { return "", fmt.Errorf("WFP 仅支持 Windows") }
func openDriver() (driver, error)          { return nil, fmt.Errorf("WFP 仅支持 Windows") }
func queryRedirect(net.Conn) ([]byte, []byte, error) {
	return nil, nil, fmt.Errorf("WFP 仅支持 Windows")
}
func socketControl(ctx context.Context) func(context.Context, string, string, syscall.RawConn) error {
	return func(context.Context, string, string, syscall.RawConn) error {
		if len(Records(ctx)) != 0 {
			return fmt.Errorf("WFP 重定向记录仅支持 Windows")
		}
		return nil
	}
}
