//go:build linux || darwin || openharmony

package mobile

import (
	"encoding/json"
	"fmt"
	"github.com/gofrs/flock"
	"io"
	"os"
	"path/filepath"
)

type Store struct{ dir string }

func OpenStore(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == string(filepath.Separator) {
		return nil, fmt.Errorf("配置目录必须是应用沙箱内的绝对路径")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}
	return &Store{dir: filepath.Clean(dir)}, nil
}

func readJSON(path string, dst any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("文件超过大小限制")
	}
	return json.Unmarshal(data, dst)
}

func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".appforce-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) load() (Config, error) {
	c := defaultConfig()
	err := readJSON(filepath.Join(s.dir, "config.json"), &c, 32<<20)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("读取手机配置失败: %w", err)
	}
	return c, c.validate()
}

func (s *Store) locked(fn func(*Config) error, write bool) (Config, error) {
	lock := flock.New(filepath.Join(s.dir, "config.lock"))
	if err := lock.Lock(); err != nil {
		return Config{}, err
	}
	defer lock.Close()
	c, err := s.load()
	if err != nil {
		return c, err
	}
	if write {
		vpnLock := flock.New(filepath.Join(s.dir, "vpn.lock"))
		defer vpnLock.Close()
		available, err := vpnLock.TryLock()
		if err != nil {
			return c, err
		}
		if !available {
			return c, fmt.Errorf("请先停止代理，再修改节点或应用范围")
		}
		if err := fn(&c); err != nil {
			return c, err
		}
		if err := c.validate(); err != nil {
			return c, err
		}
		c.Revision++
		if err := atomicJSON(filepath.Join(s.dir, "config.json"), c); err != nil {
			return c, fmt.Errorf("保存手机配置失败: %w", err)
		}
	} else if fn != nil {
		if err := fn(&c); err != nil {
			return c, err
		}
	}
	return c, nil
}
