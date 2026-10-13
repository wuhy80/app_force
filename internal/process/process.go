package process

import (
	"context"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v3/process"
)

type Info struct {
	PID        int32  `json:"pid"`
	ParentPID  int32  `json:"parent_pid"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Selectable bool   `json:"selectable"`
}

func List(ctx context.Context, search string) ([]Info, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Info, 0, len(processes))
	search = strings.ToLower(search)
	for _, p := range processes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		name, _ := p.NameWithContext(ctx)
		path, _ := p.ExeWithContext(ctx)
		parent, _ := p.PpidWithContext(ctx)
		if name == "" {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(name+" "+path), search) {
			continue
		}
		result = append(result, Info{p.Pid, parent, name, path, path != ""})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].PID < result[j].PID
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}
