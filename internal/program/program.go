// Package program browses executable files without requiring running processes.
package program

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"appforce/internal/model"
)

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type Listing struct {
	Directory string  `json:"directory"`
	Parent    string  `json:"parent"`
	Entries   []Entry `json:"entries"`
}

func Browse(ctx context.Context, directory string) (Listing, error) {
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		directory = defaultDirectory()
	}
	if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\x00\r\n") {
		return Listing{}, fmt.Errorf("请填写程序所在目录的绝对路径")
	}
	directory = filepath.Clean(directory)
	files, err := os.ReadDir(directory)
	if err != nil {
		return Listing{}, fmt.Errorf("无法读取程序目录: %w", err)
	}
	listing := Listing{Directory: directory, Parent: filepath.Dir(directory), Entries: []Entry{}}
	if listing.Parent == directory {
		listing.Parent = ""
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return Listing{}, err
		}
		path := filepath.Join(directory, file.Name())
		info, err := os.Stat(path) // Follow links so linked applications can also be selected.
		if err != nil {
			continue
		}
		isDirectory := info.IsDir()
		if isDirectory && model.IsAppBundle(path) {
			isDirectory = false
		} else if !isDirectory && (!info.Mode().IsRegular() || !isExecutable(file.Name(), info.Mode())) {
			continue
		}
		listing.Entries = append(listing.Entries, Entry{Name: file.Name(), Path: path, Directory: isDirectory})
	}
	sort.Slice(listing.Entries, func(i, j int) bool {
		a, b := listing.Entries[i], listing.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return listing, nil
}

func isExecutable(name string, mode os.FileMode) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(name), ".exe")
	}
	return mode&0111 != 0
}

func defaultDirectory() string {
	var directory string
	switch runtime.GOOS {
	case "windows":
		directory = os.Getenv("ProgramFiles")
		if directory == "" {
			directory = `C:\Program Files`
		}
	case "darwin":
		directory = "/Applications"
	default:
		directory = "/usr/bin"
	}
	if info, err := os.Stat(directory); err == nil && info.IsDir() {
		return directory
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return directory
}
