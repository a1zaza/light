// Package store owns the single-writer administration snapshot, never image blobs.
package store

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/sys/unix"
	"light/internal/jsonfile"
)

type Account struct {
	Username string `json:"username"`
	Salt     string `json:"salt"`
	Hash     string `json:"hash"`
	Revision int64  `json:"revision"`
}
type Robot struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Username   string     `json:"username"`
	Hash       string     `json:"hash,omitempty"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}
type Repository struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Public      bool      `json:"public"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type State struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Revision      int64                 `json:"revision"`
	Account       *Account              `json:"account,omitempty"`
	Secret        string                `json:"secret"`
	Robots        []Robot               `json:"robots"`
	Repositories  map[string]Repository `json:"repositories"`
}
type Audit struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	IP     string    `json:"ip"`
}
type Store struct {
	auditBroken bool
	mu          sync.RWMutex
	Dir         string
	lock        *os.File
	state       State
	frozen      bool
	auditErr    string
	auditMu     sync.Mutex
}

var ErrConflict = errors.New("数据已更新，请刷新后重试")

func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Open(dir string) (*Store, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("数据目录已被实例或离线任务占用")
	}
	s := &Store{Dir: dir, lock: lock}
	err = jsonfile.Read(filepath.Join(dir, "management.json"), &s.state)
	if errors.Is(err, os.ErrNotExist) {
		s.state = State{SchemaVersion: 1, Revision: 1, Secret: Random(48), Robots: []Robot{}, Repositories: map[string]Repository{}}
		err = jsonfile.Write(filepath.Join(dir, "management.json"), s.state)
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("管理数据读取失败：%w", err)
	}
	if s.state.SchemaVersion != 1 || len(s.state.Secret) < 32 || s.state.Repositories == nil {
		s.Close()
		return nil, errors.New("管理数据格式不受支持或不完整")
	}
	if a := s.state.Account; a != nil {
		if _, err := decodeAccount(*a); err != nil {
			s.Close()
			return nil, err
		}
	}
	for _, r := range s.state.Robots {
		if len(r.Hash) != 64 || r.ID == "" || r.Username == "" {
			s.Close()
			return nil, errors.New("机器人数据损坏")
		}
	}
	if err = jsonfile.Sync(filepath.Join(dir, "management.json")); err != nil {
		s.Close()
		return nil, fmt.Errorf("无法确认管理文件持久化状态：%w", err)
	}
	s.prepareAudit()
	return s, nil
}
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	err := s.lock.Close()
	s.lock = nil
	return err
}
func clone(in State) State {
	raw, _ := json.Marshal(in)
	var out State
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *Store) Snapshot() State { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.state) }
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.frozen && s.state.Account != nil
}
func (s *Store) AuditError() string { s.auditMu.Lock(); defer s.auditMu.Unlock(); return s.auditErr }
func (s *Store) Change(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return errors.New("持久化状态待确认，已暂停写入；请停止实例并检查磁盘后重启")
	}
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	next.Revision++
	if err := jsonfile.Write(filepath.Join(s.Dir, "management.json"), next); err != nil {
		if errors.Is(err, jsonfile.ErrUncertain) {
			s.frozen = true
		}
		return err
	}
	s.state = next
	return nil
}
func passwordAccount(username, password string, revision int64) (*Account, error) {
	if len(username) < 1 || len(username) > 64 || strings.TrimSpace(username) != username {
		return nil, errors.New("管理员名称需为 1–64 个字符且无首尾空白")
	}
	if len(password) < 8 || len(password) > 256 {
		return nil, errors.New("密码需为 8–256 字节")
	}
	salt := Random(16)
	h := argon2.IDKey([]byte(password), []byte(salt), 2, 32*1024, 2, 32)
	return &Account{Username: username, Salt: salt, Hash: base64.RawStdEncoding.EncodeToString(h), Revision: revision}, nil
}
func decodeAccount(a Account) ([]byte, error) {
	b, err := base64.RawStdEncoding.DecodeString(a.Hash)
	if err != nil || len(b) != 32 || len(a.Salt) < 16 || a.Revision < 1 || a.Username == "" {
		return nil, errors.New("管理员数据损坏")
	}
	return b, nil
}
func (s *Store) Init(username, password string) error {
	a, err := passwordAccount(username, password, 1)
	if err != nil {
		return err
	}
	return s.Change(func(st *State) error {
		if st.Account != nil {
			return errors.New("管理员已初始化")
		}
		st.Account = a
		return nil
	})
}

// AuthenticateAccount returns the exact account version whose password was
// verified. Callers must never upgrade this proof to a later account revision.
func (s *Store) AuthenticateAccount(username, password string) (Account, bool) {
	a := s.Snapshot().Account
	if a == nil {
		return Account{}, false
	}
	want, _ := decodeAccount(*a)
	got := argon2.IDKey([]byte(password), []byte(a.Salt), 2, 32*1024, 2, 32)
	if subtle.ConstantTimeCompare(got, want) != 1 || username != a.Username {
		return Account{}, false
	}
	return *a, true
}
func (s *Store) Authenticate(username, password string) bool {
	_, ok := s.AuthenticateAccount(username, password)
	return ok
}
func (s *Store) Password(username, old, password string) error {
	st := s.Snapshot()
	if !s.Authenticate(st.Account.Username, old) {
		return errors.New("原密码不正确")
	}
	a, err := passwordAccount(username, password, st.Account.Revision+1)
	if err != nil {
		return err
	}
	return s.Change(func(next *State) error {
		if next.Account.Revision != st.Account.Revision {
			return ErrConflict
		}
		next.Account = a
		return nil
	})
}
func Hash(secret string) string { h := sha256.Sum256([]byte(secret)); return hex.EncodeToString(h[:]) }
func (s *Store) RobotActive(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.state.Robots {
		if r.ID == id {
			return r.Enabled && (r.ExpiresAt == nil || r.ExpiresAt.After(time.Now()))
		}
	}
	return false
}
func (s *Store) AuthenticateRobot(username, secret string) (Robot, bool) {
	hash := Hash(secret)
	for _, r := range s.Snapshot().Robots {
		if r.Username == username && subtle.ConstantTimeCompare([]byte(hash), []byte(r.Hash)) == 1 && r.Enabled && (r.ExpiresAt == nil || r.ExpiresAt.After(time.Now())) {
			return r, true
		}
	}
	return Robot{}, false
}
func (s *Store) TouchRobot(id string) {
	_ = s.Change(func(st *State) error {
		for i, r := range st.Robots {
			if r.ID == id {
				now := time.Now().UTC()
				if r.LastUsedAt != nil && now.Sub(*r.LastUsedAt) < time.Minute {
					return errors.New("无需更新")
				}
				st.Robots[i].LastUsedAt = &now
				return nil
			}
		}
		return errors.New("机器人已撤销")
	})
}
func (s *Store) Public(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Repositories[name].Public
}
func (s *Store) Record(actor, action, target, ip string) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if s.auditBroken {
		return
	}
	if len(actor) > 128 {
		actor = actor[:128]
	}
	if len(action) > 128 {
		action = action[:128]
	}
	if len(target) > 2048 {
		target = target[:2048]
	}
	if len(ip) > 128 {
		ip = ip[:128]
	}
	dir := filepath.Join(s.Dir, "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		s.auditErr = err.Error()
		return
	}
	now := time.Now().UTC()
	path := filepath.Join(dir, "audit-"+now.In(time.Local).Format("2006-01")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.auditErr = err.Error()
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.auditErr = err.Error()
		return
	}
	if info.Size() > 16<<20 {
		s.auditErr = "当月审计已达 16 MiB 上限"
		return
	}
	raw, _ := json.Marshal(Audit{now, actor, action, target, ip})
	raw = append(raw, '\n')
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		s.auditErr = err.Error()
	}
	if err == nil {
		s.auditErr = ""
	}
	// Keep the current and previous two calendar months.
	cutoff := time.Date(now.In(time.Local).Year(), now.In(time.Local).Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -2, 0).Format("2006-01")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "audit-") && strings.HasSuffix(n, ".jsonl") && len(n) == 19 && n[6:13] < cutoff {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}
func (s *Store) Audits() ([]Audit, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	out := []Audit{}
	entries, err := os.ReadDir(filepath.Join(s.Dir, "logs"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "audit-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.Dir, "logs", e.Name()))
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(raw), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var a Audit
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				return nil, errors.New("审计文件包含损坏记录，请离线检查")
			}
			out = append(out, a)
		}
	}
	if len(out) > 1000 {
		out = out[len(out)-1000:]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Only a trailing incomplete record may be discarded after an interrupted append.
func (s *Store) prepareAudit() {
	dir := filepath.Join(s.Dir, "logs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.auditErr = err.Error()
		s.auditBroken = true
		return
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "audit-") && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		f, e := os.OpenFile(path, os.O_RDWR, 0600)
		if e != nil {
			s.auditErr = e.Error()
			s.auditBroken = true
			return
		}
		info, e := f.Stat()
		if e != nil || info.Size() > 17<<20 {
			f.Close()
			s.auditErr = "审计文件超过上限或无法读取"
			s.auditBroken = true
			return
		}
		raw := make([]byte, info.Size())
		_, e = f.ReadAt(raw, 0)
		if e != nil && len(raw) > 0 {
			f.Close()
			s.auditErr = e.Error()
			s.auditBroken = true
			return
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			if i != len(names)-1 {
				f.Close()
				s.auditErr = "历史审计文件尾行不完整，请离线检查"
				s.auditBroken = true
				return
			}
			end := bytes.LastIndexByte(raw, '\n') + 1
			if e = f.Truncate(int64(end)); e == nil {
				e = f.Sync()
			}
			if e != nil {
				f.Close()
				s.auditErr = e.Error()
				s.auditBroken = true
				return
			}
			raw = raw[:end]
		}
		f.Close()
		scan := bufio.NewScanner(bytes.NewReader(raw))
		scan.Buffer(make([]byte, 4096), 64<<10)
		for scan.Scan() {
			var a Audit
			if json.Unmarshal(scan.Bytes(), &a) != nil {
				s.auditErr = "审计文件包含损坏记录，请离线检查"
				s.auditBroken = true
				return
			}
		}
		if e = scan.Err(); e != nil {
			s.auditErr = e.Error()
			s.auditBroken = true
			return
		}
	}
}
