//go:build darwin

package antigravity

import "testing"

func TestDirectoryServiceUserForStaticAgent(t *testing.T) {
	account, err := parseDirectoryUser("developer", "NFSHomeDirectory: /Users/Developer Space\nUniqueID: 501\n")
	if err != nil || account.Uid != "501" || account.HomeDir != "/Users/Developer Space" {
		t.Fatal("Directory Service record was not parsed")
	}
	if _, err = parseDirectoryUser("developer", "UniqueID: -1\nNFSHomeDirectory: relative\n"); err == nil {
		t.Fatal("invalid Directory Service home accepted")
	}
}
