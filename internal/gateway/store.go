package gateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"netease2api/internal/netease"
)

type AccountView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SDKUID    string    `json:"sdk_uid"`
	Channel   string    `json:"channel"`
	Enabled   bool      `json:"enabled"`
	Status    string    `json:"status"`
	Busy      bool      `json:"busy"`
	Created   time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used_at"`
	Cooldown  time.Time `json:"cooldown_until"`
	Success   int       `json:"success_count"`
	Failed    int       `json:"failure_count"`
	LastError string    `json:"last_error"`
	Latency   int64     `json:"last_latency_ms"`
}
type entry struct {
	AccountView
	Source      json.RawMessage `json:"source"`
	Fingerprint string          `json:"fingerprint"`
	login       *netease.Account
	loginAt     time.Time
}
type KeyView struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Prefix  string    `json:"prefix"`
	Enabled bool      `json:"enabled"`
	Created time.Time `json:"created_at"`
}
type apiKey struct {
	KeyView
	Hash string `json:"hash"`
}
type RequestLog struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
	Latency     int64     `json:"latency_ms"`
	At          time.Time `json:"at"`
}
type vault struct {
	Version  int          `json:"version"`
	Accounts []*entry     `json:"accounts"`
	Keys     []*apiKey    `json:"keys"`
	Logs     []RequestLog `json:"logs"`
}
type Store struct {
	mu     sync.Mutex
	dir    string
	aead   cipher.AEAD
	data   vault
	next   int
	notify chan struct{}
}

func randomSecret(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func digest(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "master.key")
	master, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, vaultErr := os.Stat(filepath.Join(dir, "accounts.enc")); vaultErr == nil {
			return nil, fmt.Errorf("encrypted vault exists but master.key is missing")
		}
		master = make([]byte, 32)
		if _, err = rand.Read(master); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(master)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(master)
	if len(master) != 32 {
		return nil, fmt.Errorf("master.key must contain 32 bytes")
	}
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, aead: aead, data: vault{Version: 1}, notify: make(chan struct{})}
	wire, err := os.ReadFile(filepath.Join(dir, "accounts.enc"))
	if err == nil {
		if len(wire) < aead.NonceSize() {
			return nil, fmt.Errorf("invalid encrypted vault")
		}
		plain, e := aead.Open(nil, wire[:aead.NonceSize()], wire[aead.NonceSize():], []byte("netease2api-v1"))
		if e != nil {
			return nil, fmt.Errorf("cannot decrypt vault: %w", e)
		}
		if e = json.Unmarshal(plain, &s.data); e != nil {
			return nil, e
		}
		if s.data.Version != 1 {
			return nil, fmt.Errorf("unsupported vault version")
		}
		for _, a := range s.data.Accounts {
			a.Busy = false
			if credential, err := netease.ParseCredential(a.Source); err == nil {
				a.Channel = credential.Channel
				a.Fingerprint = digest(credential.Identity)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}
func (s *Store) persistLocked() error {
	plain, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	wire := s.aead.Seal(nonce, nonce, plain, []byte("netease2api-v1"))
	return atomicWrite(filepath.Join(s.dir, "accounts.enc"), wire)
}
func (s *Store) wakeLocked() { close(s.notify); s.notify = make(chan struct{}) }

type imported struct {
	name        string
	named       bool
	source      json.RawMessage
	uid         string
	channel     string
	fingerprint string
}

// Accept raw sauth objects, wrappers, Cookie text, account records, JSON arrays,
// or JSONL. A batch is validated completely before making any changes.
func parseImports(raw string) ([]imported, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if raw == "" {
		return nil, fmt.Errorf("导入内容为空")
	}
	var values []json.RawMessage
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, fmt.Errorf("JSON 数组格式错误")
		}
	} else if json.Valid([]byte(raw)) {
		values = []json.RawMessage{json.RawMessage(raw)}
	} else {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Cookie:"))
			if line != "" {
				if !json.Valid([]byte(line)) && (strings.Contains(line, "sauth_json=") || strings.Contains(line, "Uauth=")) {
					line = strconvQuote(line)
				}
				values = append(values, json.RawMessage(line))
			}
		}
	}
	if len(values) > 1000 {
		return nil, fmt.Errorf("单次最多导入 1000 个账号")
	}
	result := make([]imported, 0, len(values))
	for i, value := range values {
		var obj map[string]json.RawMessage
		if json.Unmarshal(value, &obj) != nil {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, fmt.Errorf("第 %d 条不是有效 JSON/Cookie", i+1)
			}
			obj = map[string]json.RawMessage{"cookie": json.RawMessage(strconvQuote(text))}
		}
		var label string
		if name, exists := obj["name"]; exists && string(name) != "null" {
			if err := json.Unmarshal(name, &label); err != nil {
				return nil, fmt.Errorf("第 %d 条 name 必须是字符串或省略", i+1)
			}
		}
		label = strings.TrimSpace(label)
		named := label != ""
		cookie := []byte(value)
		if _, ok := obj["cookie"]; ok {
			cookie = obj["cookie"]
		}
		credential, err := netease.ParseCredential(cookie)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条：%s", i+1, err)
		}
		fingerprint := digest(credential.Identity)
		if label == "" {
			identifier := credential.SDKUID
			if identifier == "" {
				identifier = fingerprint[:8]
			}
			prefix := "账号 "
			if strings.HasPrefix(credential.Channel, "4399") {
				prefix = "4399 账号 "
			}
			label = prefix + identifier
		}
		if len([]rune(label)) > 80 {
			return nil, fmt.Errorf("第 %d 条名称过长", i+1)
		}
		result = append(result, imported{name: label, named: named, source: credential.Source, uid: credential.SDKUID, channel: credential.Channel, fingerprint: fingerprint})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("没有可导入账号")
	}
	return result, nil
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func (s *Store) Import(raw string) (int, int, error) {
	items, err := parseImports(raw)
	if err != nil {
		return 0, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Refuse credential replacement during a request; never mutate an active lease.
	for _, item := range items {
		for _, a := range s.data.Accounts {
			if a.Fingerprint == item.fingerprint && a.Busy {
				return 0, 0, fmt.Errorf("账号 %s 正在使用，请稍后更新", a.Name)
			}
		}
	}
	before := append([]*entry(nil), s.data.Accounts...)
	added, updated := 0, 0
	for _, item := range items {
		var found *entry
		for index, a := range s.data.Accounts {
			if a.Fingerprint == item.fingerprint {
				copy := *a
				found = &copy
				s.data.Accounts[index] = found
				break
			}
		}
		if found == nil {
			found = &entry{AccountView: AccountView{ID: uuid.NewString(), Created: time.Now(), Enabled: true, Status: "unverified"}, Fingerprint: item.fingerprint}
			s.data.Accounts = append(s.data.Accounts, found)
			added++
		} else {
			updated++
		}
		if found.Name == "" || item.named {
			found.Name = item.name
		}
		found.SDKUID = item.uid
		found.Channel = item.channel
		found.Source = bytes.Clone(item.source)
		found.login = nil
		found.loginAt = time.Time{}
		found.LastError = ""
		found.Cooldown = time.Time{}
		found.Status = "unverified"
	}
	if err = s.persistLocked(); err != nil {
		s.data.Accounts = before
		return 0, 0, err
	}
	s.wakeLocked()
	return added, updated, nil
}
func (s *Store) Accounts() []AccountView {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]AccountView, 0, len(s.data.Accounts))
	for _, a := range s.data.Accounts {
		v := a.AccountView
		if !v.Enabled {
			v.Status = "disabled"
		} else if v.Busy {
			v.Status = "busy"
		} else if time.Now().Before(v.Cooldown) {
			v.Status = "cooldown"
		} else if v.Status == "error" {
			v.Status = "unverified"
			if a.login != nil {
				v.Status = "ready"
			}
		}
		result = append(result, v)
	}
	return result
}
func (s *Store) Update(id string, enabled *bool, remove bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.data.Accounts {
		if a.ID == id {
			if a.Busy {
				return fmt.Errorf("账号正在使用")
			}
			before := append([]*entry(nil), s.data.Accounts...)
			old := a.Enabled
			if remove {
				s.data.Accounts = append(s.data.Accounts[:i:i], s.data.Accounts[i+1:]...)
			} else if enabled != nil {
				a.Enabled = *enabled
			} else {
				return fmt.Errorf("enabled 字段必填")
			}
			if err := s.persistLocked(); err != nil {
				s.data.Accounts = before
				a.Enabled = old
				return err
			}
			s.wakeLocked()
			return nil
		}
	}
	return os.ErrNotExist
}

var ErrUnavailable = errors.New("没有可用账号，请导入/检查账号或等待冷却结束")

func (s *Store) acquire(ctx context.Context, specific string, excluded map[string]bool) (*entry, error) {
	for {
		s.mu.Lock()
		busy := false
		n := len(s.data.Accounts)
		for k := 0; k < n; k++ {
			idx := (s.next + k) % n
			a := s.data.Accounts[idx]
			if specific != "" && a.ID != specific {
				continue
			}
			if specific == "" && (!a.Enabled || a.Status == "login_required" || time.Now().Before(a.Cooldown) || excluded[a.ID]) {
				continue
			}
			if a.Busy {
				busy = true
				continue
			}
			a.Busy = true
			s.next = (idx + 1) % n
			s.mu.Unlock()
			return a, nil
		}
		ch := s.notify
		s.mu.Unlock()
		if !busy {
			return nil, ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		}
	}
}
func (s *Store) release(a *entry, login *netease.Account, loginAt time.Time, took time.Duration, err error, reqID string, check bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Busy = false
	a.login = login
	a.loginAt = loginAt
	if login != nil && login.SDKUID != "" {
		a.SDKUID = login.SDKUID
	}
	a.LastUsed = time.Now()
	a.Latency = took.Milliseconds()
	if err == nil {
		a.Status = "ready"
		a.LastError = ""
		a.Cooldown = time.Time{}
		if !check {
			a.Success++
		}
	}
	if err != nil {
		a.Failed++
		a.LastError = publicError(err)
		a.Status = "error"
		var failure *Failure
		if errors.As(err, &failure) && failure.Kind == "login" {
			a.Status = "login_required"
			a.login = nil
		}
		if errors.As(err, &failure) && failure.Kind == "quota" {
			a.Cooldown = time.Now().Add(30 * time.Minute)
		} else if a.Status != "login_required" && !errors.Is(err, context.Canceled) {
			a.Cooldown = time.Now().Add(30 * time.Second)
		}
	}
	if !check {
		s.data.Logs = append([]RequestLog{{ID: reqID, AccountID: a.ID, AccountName: a.Name, OK: err == nil, Error: publicError(err), Latency: a.Latency, At: time.Now()}}, s.data.Logs...)
		if len(s.data.Logs) > 200 {
			s.data.Logs = s.data.Logs[:200]
		}
	}
	if e := s.persistLocked(); e != nil {
		log.Printf("state save failed: %v", e)
	}
	s.wakeLocked()
}
func publicError(err error) string {
	if err == nil {
		return ""
	}
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	if errors.Is(err, context.Canceled) {
		return "请求已取消"
	}
	return "上游请求失败，请检查账号或服务连接"
}

func (s *Store) Keys() []KeyView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]KeyView, 0, len(s.data.Keys))
	for _, k := range s.data.Keys {
		out = append(out, k.KeyView)
	}
	return out
}
func (s *Store) EnsureDefaultKey(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Keys) > 0 {
		return nil
	}
	s.data.Keys = append(s.data.Keys, &apiKey{KeyView: KeyView{ID: uuid.NewString(), Name: "默认密钥", Prefix: secret[:min(14, len(secret))] + "…", Enabled: true, Created: time.Now()}, Hash: digest(secret)})
	return s.persistLocked()
}
func (s *Store) Authenticate(secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := digest(secret)
	for _, k := range s.data.Keys {
		if k.Enabled && k.Hash == hash {
			return true
		}
	}
	return false
}
func (s *Store) CreateKey(name string) (KeyView, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return KeyView{}, "", fmt.Errorf("密钥名称需为 1–80 字")
	}
	secret := randomSecret("sk-n2a-")
	k := &apiKey{KeyView: KeyView{ID: uuid.NewString(), Name: name, Prefix: secret[:14] + "…", Enabled: true, Created: time.Now()}, Hash: digest(secret)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Keys = append(s.data.Keys, k)
	if err := s.persistLocked(); err != nil {
		s.data.Keys = s.data.Keys[:len(s.data.Keys)-1]
		return KeyView{}, "", err
	}
	return k.KeyView, secret, nil
}
func (s *Store) RevokeKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.data.Keys {
		if k.ID == id {
			old := k.Enabled
			k.Enabled = false
			if err := s.persistLocked(); err != nil {
				k.Enabled = old
				return err
			}
			return nil
		}
	}
	return os.ErrNotExist
}
func (s *Store) Logs() []RequestLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestLog{}, s.data.Logs...)
}

type Access struct {
	AdminKey string `json:"admin_key"`
	APIKey   string `json:"api_key"`
}

func LoadAccess(dir string) (Access, error) {
	var access Access
	path := filepath.Join(dir, "access.local.json")
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &access); err != nil {
			return access, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		access = Access{randomSecret("admin-"), randomSecret("sk-n2a-")}
		if v := os.Getenv("NETEASE2API_ADMIN_KEY"); v != "" {
			access.AdminKey = v
		}
		if v := os.Getenv("NETEASE2API_API_KEY"); v != "" {
			access.APIKey = v
		}
		data, _ = json.MarshalIndent(access, "", "  ")
		if err = atomicWrite(path, data); err != nil {
			return access, err
		}
	} else {
		return access, err
	}
	if v := os.Getenv("NETEASE2API_ADMIN_KEY"); v != "" {
		access.AdminKey = v
	}
	if v := os.Getenv("NETEASE2API_API_KEY"); v != "" {
		access.APIKey = v
	}
	if len(access.AdminKey) < 16 || len(access.APIKey) < 16 {
		return access, fmt.Errorf("管理密钥和 API 密钥至少 16 字符")
	}
	return access, nil
}
