// Package wfp contains the versioned contract with AppForce's optional Windows
// callout driver. It never changes routes, interface DNS, or the system proxy.
package wfp

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf16"

	"appforce/internal/model"
)

const (
	Magic          = 0x46575041 // APWF, little endian
	Version        = 2
	MaxRules       = 64
	MaxPath        = 1024 // UTF-16 code units, without a terminator
	HeaderSize     = 48
	RuleSize       = 16 + MaxPath*2
	ContextSize    = 72 + MaxPath*2
	HealthSize     = 40
	SocketOutbound = "appforce-wfp-socket"
)

type Rule struct {
	ID      uint32
	Program model.Rule
	AppID   string // canonical device path returned by FwpmGetAppIdFromFileName
	Helpers bool
}

// CheckHealth binds the driver's answer to this particular installed policy,
// rather than treating an open device handle as evidence of active filtering.
func (p *Policy) CheckHealth(b []byte) error {
	u32 := binary.LittleEndian.Uint32
	if len(b) != HealthSize || u32(b) != Magic || u32(b[4:]) != Version || u32(b[8:]) != HealthSize ||
		u32(b[12:]) != 1 || string(b[16:32]) != string(p.Cookie[:]) ||
		u32(b[32:]) != uint32(len(p.Rules)) || u32(b[36:]) != 4 {
		return fmt.Errorf("WFP 驱动规则状态无效、会话已失效或版本不兼容")
	}
	return nil
}

type Policy struct {
	Cookie [16]byte
	Rules  []Rule
}

// Compile resolves only the enabled executable paths. The driver's helper scope
// is deliberately restricted to Antigravity's known language server names.
func Compile(programs []model.Rule, appID func(string) (string, error)) (*Policy, error) {
	p := &Policy{}
	if _, err := rand.Read(p.Cookie[:]); err != nil {
		return nil, err
	}
	for _, program := range programs {
		if !program.Enabled {
			continue
		}
		if len(p.Rules) == MaxRules {
			return nil, fmt.Errorf("WFP 原型最多支持 %d 个启用的程序", MaxRules)
		}
		id, err := appID(program.Path)
		if err != nil {
			return nil, fmt.Errorf("获取程序 WFP 标识 %s: %w", program.Path, err)
		}
		id = strings.ToLower(id)
		if !strings.HasPrefix(id, `\device\`) || strings.ContainsRune(id, 0) || len(utf16.Encode([]rune(id))) > MaxPath {
			return nil, fmt.Errorf("无效或过长的 WFP 程序路径: %s", program.Path)
		}
		name := id[strings.LastIndex(id, `\`)+1:]
		p.Rules = append(p.Rules, Rule{uint32(len(p.Rules) + 1), program, id, name == "antigravity.exe" || name == "antigravity ide.exe"})
	}
	if len(p.Rules) == 0 {
		return nil, fmt.Errorf("请先添加并启用至少一个程序规则")
	}
	return p, nil
}

func (p *Policy) Encode(pid uint64, port4, port6 uint16) ([]byte, error) {
	if pid == 0 || port4 == 0 || port6 == 0 || len(p.Rules) == 0 || len(p.Rules) > MaxRules {
		return nil, fmt.Errorf("无效的 WFP 会话参数")
	}
	b := make([]byte, HeaderSize+len(p.Rules)*RuleSize)
	put := binary.LittleEndian.PutUint32
	put(b, Magic)
	put(b[4:], Version)
	put(b[8:], uint32(len(b)))
	put(b[12:], uint32(len(p.Rules)))
	binary.LittleEndian.PutUint64(b[16:], pid)
	binary.LittleEndian.PutUint16(b[24:], port4)
	binary.LittleEndian.PutUint16(b[26:], port6)
	copy(b[32:], p.Cookie[:])
	for i, r := range p.Rules {
		dst := b[HeaderSize+i*RuleSize:]
		path := utf16.Encode([]rune(r.AppID))
		if len(path) == 0 || len(path) > MaxPath || r.ID != uint32(i+1) {
			return nil, fmt.Errorf("无效的 WFP 规则")
		}
		put(dst, r.ID)
		if r.Helpers {
			put(dst[4:], 1)
		}
		put(dst[8:], uint32(len(path)*2))
		for j, c := range path {
			binary.LittleEndian.PutUint16(dst[16+j*2:], c)
		}
	}
	return b, nil
}

func (r Rule) Matches(path string) bool {
	if path == r.AppID {
		return true
	}
	if !r.Helpers {
		return false
	}
	dir := r.AppID[:strings.LastIndex(r.AppID, `\`)+1]
	if !strings.HasPrefix(path, dir) {
		return false
	}
	name := path[strings.LastIndex(path, `\`)+1:]
	return name == "language_server_windows_x64.exe" || name == "language_server_windows_arm64.exe" || name == "language_server.exe"
}

type Redirect struct {
	Rule        Rule
	PID         uint32
	Destination netip.AddrPort
	ProcessPath string
}

// DecodeContext rejects foreign/stale redirects and unknown rules. The listener
// accepts only kernel-authenticated redirect contexts, never a user handshake.
func (p *Policy) DecodeContext(b []byte) (Redirect, error) {
	var result Redirect
	u32 := binary.LittleEndian.Uint32
	if len(b) != ContextSize || u32(b) != Magic || u32(b[4:]) != Version || u32(b[8:]) != ContextSize || string(b[24:40]) != string(p.Cookie[:]) {
		return result, fmt.Errorf("WFP 重定向上下文无效或版本不兼容")
	}
	id, pid, pathSize := u32(b[12:]), binary.LittleEndian.Uint64(b[16:]), u32(b[64:])
	if id == 0 || int(id) > len(p.Rules) || pid == 0 || pid > 0xffffffff || pathSize == 0 || pathSize > MaxPath*2 || pathSize%2 != 0 || u32(b[68:]) != 0 {
		return result, fmt.Errorf("WFP 重定向进程或规则无效")
	}
	path16 := make([]uint16, pathSize/2)
	for i := range path16 {
		path16[i] = binary.LittleEndian.Uint16(b[72+i*2:])
	}
	path := string(utf16.Decode(path16))
	rule := p.Rules[id-1]
	if !rule.Matches(path) {
		return result, fmt.Errorf("WFP 重定向路径超出程序规则")
	}
	family, port := binary.LittleEndian.Uint16(b[40:]), binary.LittleEndian.Uint16(b[42:])
	var addr netip.Addr
	switch family {
	case 4:
		addr = netip.AddrFrom4([4]byte(b[48:52]))
	case 6:
		addr = netip.AddrFrom16([16]byte(b[48:64]))
	default:
		return result, fmt.Errorf("WFP 重定向地址族无效")
	}
	// Only public targets are intercepted. DNS/private traffic stays with Windows.
	if port == 0 || port == 53 || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.Is4In6() || binary.LittleEndian.Uint32(b[44:]) != 0 {
		return result, fmt.Errorf("WFP 重定向目标超出代理范围")
	}
	processPath := rule.Program.Path
	if path != rule.AppID {
		ntDir := rule.AppID[:strings.LastIndex(rule.AppID, `\`)+1]
		dosDir := strings.ReplaceAll(processPath, "/", `\`)
		dosDir = dosDir[:strings.LastIndex(dosDir, `\`)+1]
		processPath = dosDir + strings.TrimPrefix(path, ntDir)
	}
	return Redirect{rule, uint32(pid), netip.AddrPortFrom(addr, port), processPath}, nil
}
