package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"appforce/internal/model"
)

type Store struct{ Dir string }

func Open(dir string) (*Store, model.Config, string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, model.Config{}, "", err
	}
	s := &Store{Dir: dir}
	tokenPath := filepath.Join(dir, "token")
	token, err := os.ReadFile(tokenPath)
	if os.IsNotExist(err) {
		token = []byte(model.NewID() + model.NewID())
		file, e := os.OpenFile(tokenPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, model.Config{}, "", e
		}
		_, e = file.Write(token)
		closeErr := file.Close()
		if e != nil {
			return nil, model.Config{}, "", e
		}
		if closeErr != nil {
			return nil, model.Config{}, "", closeErr
		}
	} else if err != nil {
		return nil, model.Config{}, "", err
	}
	if len(token) != 64 {
		return nil, model.Config{}, "", fmt.Errorf("管理密钥文件格式无效")
	}
	c := model.DefaultConfig()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return s, c, string(token), nil
	}
	if err != nil {
		return nil, c, "", err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return nil, c, "", fmt.Errorf("配置文件损坏，未覆盖原文件: %w", err)
	}
	if c.Version != 1 {
		return nil, c, "", fmt.Errorf("不支持的配置版本 %d", c.Version)
	}
	c.Settings.TUNStack = c.Settings.EffectiveTUNStack()
	if err = c.Settings.Validate(); err != nil {
		return nil, c, "", err
	}
	return s, c, string(token), nil
}

func (s *Store) Save(c model.Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
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
	return os.Rename(f.Name(), filepath.Join(s.Dir, "config.json"))
}
