package antigravity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const vaultVersion = "AppForce.Antigravity.v1"

type vaultState struct {
	Accounts    []Account    `json:"accounts"`
	OAuth       OAuthConfig  `json:"oauth"`
	Activations []Activation `json:"activations"`
}

type vault struct {
	path string
	aead cipher.AEAD
}

func openVault(dir string) (*vault, vaultState, error) {
	var state vaultState
	state.Accounts = []Account{}
	state.OAuth = defaultOAuthConfig()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, state, err
	}
	keyPath := filepath.Join(dir, "accounts.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		// Never regenerate a key for an existing vault: doing so would discard accounts.
		if _, statErr := os.Stat(filepath.Join(dir, "accounts.enc")); !os.IsNotExist(statErr) {
			return nil, state, fmt.Errorf("账号加密密钥缺失，请恢复 accounts.key")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, state, err
		}
		f, createErr := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			// Concurrent first opens use the winning key.
			if key, err = os.ReadFile(keyPath); err != nil {
				return nil, state, createErr
			}
		} else {
			_, err = f.Write(key)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return nil, state, err
			}
			if closeErr != nil {
				return nil, state, closeErr
			}
		}
	} else if err != nil {
		return nil, state, err
	}
	if len(key) != 32 {
		return nil, state, fmt.Errorf("账号加密密钥格式无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, state, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, state, err
	}
	v := &vault{filepath.Join(dir, "accounts.enc"), aead}
	sealed, err := os.ReadFile(v.path)
	if os.IsNotExist(err) {
		return v, state, nil
	}
	if err != nil {
		return nil, state, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, state, fmt.Errorf("账号文件损坏，未覆盖原文件")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(vaultVersion))
	if err != nil {
		return nil, state, fmt.Errorf("账号文件无法解密，未覆盖原文件")
	}
	if json.Unmarshal(plain, &state) != nil {
		return nil, state, fmt.Errorf("账号数据格式无效，未覆盖原文件")
	}
	return v, state, nil
}

func (v *vault) save(state vaultState) error {
	plain, err := json.Marshal(state)
	if err != nil {
		return err
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := v.aead.Seal(nonce, nonce, plain, []byte(vaultVersion))
	f, err := os.CreateTemp(filepath.Dir(v.path), ".accounts-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(sealed)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), v.path)
}

func cloneState(state vaultState) vaultState {
	// Copy through JSON so failed saves cannot mutate cached quota or activations.
	data, _ := json.Marshal(state)
	var next vaultState
	_ = json.Unmarshal(data, &next)
	return next
}
