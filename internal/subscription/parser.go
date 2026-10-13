package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"appforce/internal/model"
	"gopkg.in/yaml.v3"
)

const MaxSize = 8 << 20

type Result struct {
	Nodes    []model.Node `json:"nodes"`
	Warnings []string     `json:"warnings"`
}

var supported = map[string]bool{"shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "hysteria2": true, "socks": true, "http": true}

func Parse(data []byte, sourceID string) (Result, error) {
	result := Result{Nodes: []model.Node{}, Warnings: []string{}}
	if len(data) > MaxSize {
		return result, fmt.Errorf("订阅超过 8 MiB")
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	if len(data) == 0 {
		return result, fmt.Errorf("订阅内容为空")
	}
	seen := map[string]bool{}
	add := func(name string, outbound map[string]any, err error, position string) {
		if err == nil {
			if !supported[stringValue(outbound["type"])] {
				err = fmt.Errorf("不支持的节点协议")
			}
		}
		if err == nil {
			err = validateStandalone(outbound)
		}
		var n model.Node
		if err == nil {
			n, err = model.NewNode(name, sourceID, outbound)
		}
		if err != nil {
			result.Warnings = append(result.Warnings, position+": "+err.Error())
			return
		}
		if !seen[n.ID] {
			result.Nodes = append(result.Nodes, n)
			seen[n.ID] = true
		}
	}
	if data[0] == '{' || data[0] == '[' {
		var raw any
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if err := d.Decode(&raw); err != nil {
			return result, fmt.Errorf("sing-box JSON 格式无效")
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return result, fmt.Errorf("sing-box JSON 只能包含一个完整值")
		}
		var outbounds []any
		switch v := raw.(type) {
		case map[string]any:
			outbounds, _ = v["outbounds"].([]any)
			if outbounds == nil && v["type"] != nil {
				outbounds = []any{v}
			}
		case []any:
			outbounds = v
		}
		if outbounds == nil {
			return result, fmt.Errorf("JSON 中没有代理 outbounds")
		}
		for i, value := range outbounds {
			outbound, ok := value.(map[string]any)
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("节点 %d: 不是对象", i+1))
				continue
			}
			if typ := stringValue(outbound["type"]); typ == "direct" || typ == "block" || typ == "selector" || typ == "urltest" || typ == "dns" {
				continue
			}
			add(stringValue(outbound["tag"]), outbound, nil, fmt.Sprintf("节点 %d", i+1))
		}
	} else if bytes.Contains(data, []byte("proxies:")) {
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return result, fmt.Errorf("Clash YAML 格式无效")
		}
		for i, proxy := range doc.Proxies {
			outbound, err := parseClash(proxy)
			add(stringValue(proxy["name"]), outbound, err, fmt.Sprintf("节点 %d", i+1))
		}
	} else {
		text := string(data)
		if !strings.Contains(text, "://") {
			decoded, err := decode64(strings.Join(strings.Fields(text), ""))
			if err != nil {
				return result, fmt.Errorf("无法识别订阅格式")
			}
			text = string(decoded)
		}
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			name, outbound, err := parseURI(line)
			add(name, outbound, err, fmt.Sprintf("第 %d 行", i+1))
		}
	}
	if len(result.Nodes) == 0 {
		return result, fmt.Errorf("没有可导入的节点；%s", strings.Join(result.Warnings, "；"))
	}
	return result, nil
}

func validateStandalone(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, nested := range v {
			if strings.HasSuffix(key, "_path") || key == "certificate_directory" || key == "providers" {
				return fmt.Errorf("不支持订阅中的本地文件引用 %s", key)
			}
			if err := validateStandalone(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range v {
			if err := validateStandalone(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func decode64(text string) ([]byte, error) {
	text = strings.TrimRight(text, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("base64 格式无效")
}

func parseURI(text string) (string, map[string]any, error) {
	if strings.HasPrefix(text, "vmess://") {
		return parseVMess(strings.TrimPrefix(text, "vmess://"))
	}
	if strings.HasPrefix(text, "ss://") {
		body := strings.TrimPrefix(text, "ss://")
		beforeFragment := strings.SplitN(body, "#", 2)[0]
		if !strings.Contains(beforeFragment, "@") {
			decoded, err := decode64(beforeFragment)
			if err != nil {
				return "", nil, err
			}
			text = "ss://" + string(decoded)
			if parts := strings.SplitN(body, "#", 2); len(parts) == 2 {
				text += "#" + parts[1]
			}
		}
	}
	u, err := url.Parse(text)
	if err != nil {
		return "", nil, fmt.Errorf("分享链接格式无效")
	}
	typ := u.Scheme
	switch typ {
	case "ss":
		typ = "shadowsocks"
	case "hy2":
		typ = "hysteria2"
	case "socks5":
		typ = "socks"
	}
	if !supported[typ] {
		return "", nil, fmt.Errorf("不支持的分享链接协议")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || u.Hostname() == "" {
		return "", nil, fmt.Errorf("分享链接缺少服务器或有效端口")
	}
	q := u.Query()
	allowed := map[string]bool{}
	parameters := "udp"
	switch typ {
	case "vless":
		parameters += " type security sni peer serverName allowInsecure insecure fp pbk sid flow encryption packetEncoding path host serviceName service-name alpn"
	case "trojan":
		parameters += " type security sni peer serverName allowInsecure insecure fp path host serviceName service-name alpn"
	case "hysteria2":
		parameters += " security sni peer serverName allowInsecure insecure alpn obfs obfs-password mport"
	case "http":
		parameters += " security sni peer serverName allowInsecure insecure alpn"
	}
	for _, key := range strings.Fields(parameters) {
		allowed[key] = true
	}
	for key := range q {
		if !allowed[key] {
			return "", nil, fmt.Errorf("不支持分享链接参数 %s", key)
		}
	}
	if u.Path != "" && u.Path != "/" {
		return "", nil, fmt.Errorf("不支持分享链接中的 URL 路径，请使用 path 参数")
	}
	out := map[string]any{"type": typ, "server": u.Hostname(), "server_port": port}
	if u.User == nil && typ != "socks" && typ != "http" {
		return "", nil, fmt.Errorf("分享链接缺少认证信息")
	}
	user, password, hasPassword := "", "", false
	if u.User != nil {
		user = u.User.Username()
		password, hasPassword = u.User.Password()
	}
	switch typ {
	case "shadowsocks":
		if !hasPassword {
			decoded, e := decode64(user)
			if e != nil {
				return "", nil, e
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) != 2 {
				return "", nil, fmt.Errorf("Shadowsocks 认证格式无效")
			}
			user, password = parts[0], parts[1]
		}
		out["method"], out["password"] = user, password
	case "vless":
		out["uuid"] = user
		if value := q.Get("flow"); value != "" {
			out["flow"] = value
		}
		if value := q.Get("encryption"); value != "" && value != "none" {
			return "", nil, fmt.Errorf("暂不支持 VLESS encryption")
		}
		if value := q.Get("packetEncoding"); value != "" {
			out["packet_encoding"] = value
		}
	case "trojan", "hysteria2":
		if hasPassword {
			user += ":" + password
		}
		out["password"] = user
	case "socks", "http":
		out["username"], out["password"] = user, password
		if typ == "socks" {
			out["version"] = "5"
		}
	}
	security := q.Get("security")
	if security == "" && (typ == "trojan" || typ == "hysteria2") {
		security = "tls"
	}
	if security != "reality" && (q.Has("pbk") || q.Has("sid")) {
		return "", nil, fmt.Errorf("Reality 参数需要 security=reality")
	}
	if security == "" || security == "none" {
		for _, key := range strings.Fields("sni peer serverName allowInsecure insecure fp alpn") {
			if q.Has(key) {
				return "", nil, fmt.Errorf("参数 %s 需要开启 TLS", key)
			}
		}
	}
	if security != "" && security != "none" {
		if security != "tls" && security != "reality" {
			return "", nil, fmt.Errorf("不支持的 TLS 类型")
		}
		tls := map[string]any{"enabled": true}
		for _, key := range []string{"sni", "peer", "serverName"} {
			if q.Get(key) != "" {
				tls["server_name"] = q.Get(key)
				break
			}
		}
		for _, key := range []string{"allowInsecure", "insecure"} {
			if q.Get(key) != "" {
				tls["insecure"] = q.Get(key) == "1" || q.Get(key) == "true"
			}
		}
		if value := q.Get("alpn"); value != "" {
			tls["alpn"] = strings.Split(value, ",")
		}
		if value := q.Get("fp"); value != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": value}
		}
		if security == "reality" {
			if q.Get("pbk") == "" {
				return "", nil, fmt.Errorf("Reality 缺少 public key")
			}
			tls["reality"] = map[string]any{"enabled": true, "public_key": q.Get("pbk"), "short_id": q.Get("sid")}
			if tls["utls"] == nil {
				tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
			}
		}
		out["tls"] = tls
	}
	if err := addTransport(out, q.Get("type"), q.Get("path"), q.Get("host"), first(q.Get("serviceName"), q.Get("service-name"))); err != nil {
		return "", nil, err
	}
	if typ == "hysteria2" {
		if q.Get("obfs") != "" {
			out["obfs"] = map[string]any{"type": q.Get("obfs"), "password": q.Get("obfs-password")}
		}
		if q.Get("mport") != "" {
			return "", nil, fmt.Errorf("首版暂不支持 Hysteria2 端口跳跃")
		}
	}
	if q.Get("udp") == "0" || q.Get("udp") == "false" {
		out["network"] = "tcp"
	}
	return u.Fragment, out, nil
}

func parseVMess(body string) (string, map[string]any, error) {
	b, err := decode64(body)
	if err != nil {
		return "", nil, err
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return "", nil, fmt.Errorf("VMess JSON 无效")
	}
	allowed := strings.Fields("v ps add port id aid scy net type host path tls sni alpn fp allowInsecure")
	if err := unknownFields(raw, allowed); err != nil {
		return "", nil, err
	}
	port, err := model.Number(raw["port"])
	if err != nil {
		return "", nil, err
	}
	out := map[string]any{"type": "vmess", "server": raw["add"], "server_port": port, "uuid": raw["id"], "security": first(stringValue(raw["scy"]), "auto")}
	if aid := raw["aid"]; aid != nil {
		number, e := model.Number(aid)
		if e != nil {
			return "", nil, e
		}
		out["alter_id"] = number
	}
	if header := stringValue(raw["type"]); header != "" && header != "none" {
		return "", nil, fmt.Errorf("不支持 VMess 伪装类型")
	}
	transportType, transportPath, serviceName := stringValue(raw["net"]), stringValue(raw["path"]), ""
	if transportType == "grpc" {
		serviceName, transportPath = transportPath, ""
	}
	if err := addTransport(out, transportType, transportPath, stringValue(raw["host"]), serviceName); err != nil {
		return "", nil, err
	}
	if stringValue(raw["tls"]) == "tls" {
		tls := map[string]any{"enabled": true}
		if v := stringValue(raw["sni"]); v != "" {
			tls["server_name"] = v
		}
		if v := stringValue(raw["alpn"]); v != "" {
			tls["alpn"] = strings.Split(v, ",")
		}
		if v := stringValue(raw["fp"]); v != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": v}
		}
		if v := raw["allowInsecure"]; v != nil {
			tls["insecure"] = v == true || stringValue(v) == "1" || stringValue(v) == "true"
		}
		out["tls"] = tls
	} else if tls := stringValue(raw["tls"]); tls != "" && tls != "none" {
		return "", nil, fmt.Errorf("不支持 VMess TLS 类型")
	}
	return stringValue(raw["ps"]), out, nil
}

func parseClash(raw map[string]any) (map[string]any, error) {
	if err := unknownFields(raw, strings.Fields("name type server port cipher password uuid alterId alter-id security flow tls servername sni skip-cert-verify client-fingerprint fingerprint alpn reality-opts network ws-opts grpc-opts h2-opts http-opts udp tfo username obfs obfs-password up down")); err != nil {
		return nil, err
	}
	typ := stringValue(raw["type"])
	switch typ {
	case "ss":
		typ = "shadowsocks"
	case "socks5":
		typ = "socks"
	case "hy2":
		typ = "hysteria2"
	}
	out := map[string]any{"type": typ, "server": raw["server"], "server_port": raw["port"]}
	switch typ {
	case "shadowsocks":
		out["method"], out["password"] = raw["cipher"], raw["password"]
	case "vmess":
		out["uuid"], out["security"] = raw["uuid"], first(stringValue(raw["cipher"]), stringValue(raw["security"]), "auto")
		if v := raw["alterId"]; v != nil {
			out["alter_id"] = v
		} else if v := raw["alter-id"]; v != nil {
			out["alter_id"] = v
		}
	case "vless":
		out["uuid"] = raw["uuid"]
		if raw["flow"] != nil {
			out["flow"] = raw["flow"]
		}
	case "trojan", "hysteria2":
		out["password"] = raw["password"]
	case "socks", "http":
		for _, key := range []string{"username", "password"} {
			if raw[key] != nil {
				out[key] = raw[key]
			}
		}
		if typ == "socks" {
			out["version"] = "5"
		}
	default:
		return nil, fmt.Errorf("不支持 Clash 协议 %s", typ)
	}
	if raw["udp"] == false {
		out["network"] = "tcp"
	}
	if raw["tfo"] != nil {
		out["tcp_fast_open"] = raw["tfo"]
	}
	_, reality := raw["reality-opts"]
	if raw["tls"] == true || typ == "trojan" || typ == "hysteria2" || reality {
		tls := map[string]any{"enabled": true}
		if serverName := first(stringValue(raw["servername"]), stringValue(raw["sni"])); serverName != "" {
			tls["server_name"] = serverName
		}
		if raw["skip-cert-verify"] != nil {
			tls["insecure"] = raw["skip-cert-verify"]
		}
		if raw["alpn"] != nil {
			tls["alpn"] = raw["alpn"]
		}
		if fp := first(stringValue(raw["client-fingerprint"]), stringValue(raw["fingerprint"])); fp != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if reality {
			opts, ok := raw["reality-opts"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Reality 配置无效")
			}
			if err := unknownFields(opts, []string{"public-key", "short-id"}); err != nil {
				return nil, err
			}
			tls["reality"] = map[string]any{"enabled": true, "public_key": opts["public-key"], "short_id": first(stringValue(opts["short-id"]), "")}
			if tls["utls"] == nil {
				tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
			}
		}
		out["tls"] = tls
	}
	network := stringValue(raw["network"])
	var path, host, serviceName string
	switch network {
	case "ws":
		if opts, ok := raw["ws-opts"].(map[string]any); ok {
			if err := unknownFields(opts, []string{"path", "headers"}); err != nil {
				return nil, err
			}
			path = stringValue(opts["path"])
			if headers, ok := opts["headers"].(map[string]any); ok {
				if err := unknownFields(headers, []string{"Host"}); err != nil {
					return nil, err
				}
				host = stringValue(headers["Host"])
			}
		}
	case "grpc":
		if opts, ok := raw["grpc-opts"].(map[string]any); ok {
			if err := unknownFields(opts, []string{"grpc-service-name"}); err != nil {
				return nil, err
			}
			serviceName = stringValue(opts["grpc-service-name"])
		}
	case "h2", "http":
		return nil, fmt.Errorf("Clash HTTP/H2 传输暂不支持，请导入 sing-box JSON")
	}
	if err := addTransport(out, network, path, host, serviceName); err != nil {
		return nil, err
	}
	if typ == "hysteria2" {
		if raw["obfs"] != nil {
			out["obfs"] = map[string]any{"type": raw["obfs"], "password": raw["obfs-password"]}
		}
		for _, direction := range []string{"up", "down"} {
			if raw[direction] != nil {
				speed, e := model.Number(raw[direction])
				if e != nil {
					return nil, fmt.Errorf("Hysteria2 带宽必须使用 Mbps 整数")
				}
				out[direction+"_mbps"] = speed
			}
		}
	}
	return out, nil
}

func addTransport(out map[string]any, typ, path, host, serviceName string) error {
	switch typ {
	case "", "tcp":
		if path != "" || host != "" || serviceName != "" {
			return fmt.Errorf("TCP 传输不支持路径或伪装参数")
		}
	case "ws":
		if serviceName != "" {
			return fmt.Errorf("WebSocket 不支持 gRPC serviceName")
		}
		transport := map[string]any{"type": "ws", "path": path}
		if host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		out["transport"] = transport
	case "grpc":
		if host != "" || path != "" {
			return fmt.Errorf("首版暂不支持 gRPC authority/host/path")
		}
		out["transport"] = map[string]any{"type": "grpc", "service_name": serviceName}
	case "http", "h2":
		if serviceName != "" {
			return fmt.Errorf("HTTP 不支持 gRPC serviceName")
		}
		transport := map[string]any{"type": "http", "path": path}
		if host != "" {
			transport["host"] = strings.Split(host, ",")
		}
		out["transport"] = transport
	default:
		return fmt.Errorf("不支持传输方式 %s", typ)
	}
	return nil
}

func unknownFields(raw map[string]any, allowed []string) error {
	keys := map[string]bool{}
	for _, key := range allowed {
		keys[key] = true
	}
	for key := range raw {
		if !keys[key] {
			return fmt.Errorf("不支持字段 %s", key)
		}
	}
	return nil
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
