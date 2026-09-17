package process

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/classify"
	"github.com/bingame/cli-relay/internal/provider"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/bingame/cli-relay/internal/secrets"
	"golang.org/x/term"
)

type Session struct {
	CLI       string `json:"cli"`
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	WorkDir   string `json:"work_dir"`
	StartedAt string `json:"started_at"`
}
type Instance struct {
	Session
	PID  int    `json:"pid"`
	Mode string `json:"mode"`
}
type Options struct {
	Inputs                      adapter.LaunchInputs
	Stdin                       io.Reader
	Stdout, Stderr              io.Writer
	Dir, Root, Target, Provider string
	SecretValues                []string
	OnSession                   func(Session) error
	ProtocolBridge              bool
	SessionDir                  string
}
type Result struct {
	ExitCode       int
	NativeExitCode int
	SessionID      string
	FinalText      string
}

func Environment(base []string, unset []string, overlay map[string]string) []string {
	m := map[string]string{}
	norm := func(s string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(s)
		}
		return s
	}
	for _, v := range base {
		if i := strings.IndexByte(v, '='); i > 0 {
			m[norm(v[:i])] = v
		}
	}
	for _, k := range unset {
		delete(m, norm(k))
	}
	delete(m, norm("RELAY_PASSPHRASE"))
	for k, v := range overlay {
		m[norm(k)] = k + "=" + v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}
func IsTTY() bool { return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) }

func Configure(cmd *exec.Cmd) { configure(cmd) }

func Manage(cmd *exec.Cmd) (func(), error) {
	stop, e := manage(cmd)
	if e != nil {
		return nil, e
	}
	var once sync.Once
	return func() { once.Do(stop) }, nil
}

func Spawn(ctx context.Context, o Options) (Result, error) {
	result := Result{}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Dir == "" {
		o.Dir, _ = os.Getwd()
	}
	binary, resolveErr := ResolveBinary(o.Inputs.Binary)
	if resolveErr != nil {
		return result, resolveErr
	}
	cmd := exec.CommandContext(ctx, binary, o.Inputs.Args...)
	cmd.Dir = o.Dir
	cmd.Env = Environment(os.Environ(), o.Inputs.UnsetEnv, o.Inputs.Env)
	cmd.Stdin = o.Stdin
	configure(cmd)
	cmd.WaitDelay = 3 * time.Second
	var mu sync.Mutex
	code := 0
	failed := false
	completed := false
	var metadataErr error
	started := time.Now().UTC().Format(time.RFC3339Nano)
	sessionDir := o.SessionDir
	if sessionDir == "" {
		sessionDir = o.Dir
	}
	recorded := map[string]bool{}
	observe := func(line []byte) {
		e := classify.Parse(line)
		mu.Lock()
		defer mu.Unlock()
		code = classify.Merge(code, e.Code)
		failed = failed || e.Failed
		if e.Failed {
			completed = false
		}
		if e.Completed {
			completed = true
		}
		if e.Text != "" {
			result.FinalText = secrets.Redact(e.Text, o.SecretValues)
		}
		if e.WorkDir != "" && filepath.IsAbs(e.WorkDir) {
			sessionDir = e.WorkDir
		}
		if e.SessionID != "" && provider.ValidID(e.SessionID) {
			result.SessionID = e.SessionID
			if recorded[e.SessionID] {
				return
			}
			recorded[e.SessionID] = true
			s := Session{o.Target, o.Provider, e.SessionID, sessionDir, started}
			var err error
			if o.Root != "" {
				err = SaveSession(o.Root, s)
			}
			if err == nil && o.OnSession != nil {
				err = o.OnSession(s)
			}
			if err != nil {
				metadataErr = err
			}
		}
	}
	stdout := newStream(o.Stdout, o.SecretValues, observe)
	stderr := newStream(o.Stderr, o.SecretValues, nil)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if e := cmd.Start(); e != nil {
		return result, fmt.Errorf("启动 %s 失败: %w", o.Target, e)
	}
	stopTree, e := Manage(cmd)
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return result, e
	}
	defer stopTree()
	cleanup, e := track(o, cmd.Process.Pid, "exec", started)
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return result, e
	}
	defer cleanup()
	err := cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() && (completed || o.ProtocolBridge) {
		stopTree()
		err = nil
	}
	outErr := stdout.Close()
	errErr := stderr.Close()
	if err != nil {
		if x, ok := err.(*exec.ExitError); ok {
			result.NativeExitCode = x.ExitCode()
			if result.NativeExitCode < 0 {
				result.NativeExitCode = 1
			}
		} else {
			return result, fmt.Errorf("监管进程失败: %w", err)
		}
	}
	if result.NativeExitCode != 0 {
		code = classify.Merge(code, classify.Error(stderr.tail))
	}
	result.ExitCode = code
	if result.ExitCode == 0 {
		result.ExitCode = result.NativeExitCode
	}
	if failed && result.ExitCode == 0 {
		result.ExitCode = 1
	}
	if completed && result.NativeExitCode == 0 && code != classify.NeedsHuman {
		result.ExitCode = 0
	}
	if o.ProtocolBridge {
		result.ExitCode = result.NativeExitCode
	}
	if metadataErr != nil {
		return result, fmt.Errorf("保存会话元数据失败: %w", metadataErr)
	}
	if outErr != nil {
		return result, outErr
	}
	if errErr != nil {
		return result, errErr
	}
	return result, nil
}
func SaveSession(root string, s Session) error {
	if !provider.ValidID(s.CLI) || !provider.ValidID(s.SessionID) {
		return fmt.Errorf("会话 ID 非法")
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return safeio.WriteFile(filepath.Join(root, "sessions", s.CLI, s.SessionID+".meta.json"), b, 0600)
}
func LoadSession(root, target, id string) (Session, error) {
	var s Session
	if !provider.ValidID(target) || !provider.ValidID(id) {
		return s, fmt.Errorf("会话 ID 非法")
	}
	b, e := safeio.ReadRegular(filepath.Join(root, "sessions", target, id+".meta.json"), 1<<20)
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func track(o Options, pid int, mode, started string) (func(), error) {
	if o.Root == "" {
		return func() {}, nil
	}
	id := make([]byte, 12)
	if _, e := rand.Read(id); e != nil {
		return nil, e
	}
	p := filepath.Join(o.Root, "instances", hex.EncodeToString(id)+".json")
	b, _ := json.Marshal(Instance{Session: Session{CLI: o.Target, Provider: o.Provider, WorkDir: o.Dir, StartedAt: started}, PID: pid, Mode: mode})
	e := safeio.WriteFile(p, b, 0600)
	return func() { _ = os.Remove(p) }, e
}
func Instances(root string) ([]Instance, error) {
	out := []Instance{}
	entries, e := os.ReadDir(filepath.Join(root, "instances"))
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, e := safeio.ReadRegular(filepath.Join(root, "instances", entry.Name()), 1<<20)
		if e != nil {
			return nil, e
		}
		var v Instance
		if json.Unmarshal(b, &v) != nil {
			continue
		}
		if Alive(v.PID) {
			out = append(out, v)
		}
	}
	return out, nil
}

// stream 边转发边提取有界 JSONL，不因超长 thinking 行中断管道；跨写入片段脱敏。
type stream struct {
	dst     io.Writer
	values  []string
	pending []byte
	line    []byte
	discard bool
	observe func([]byte)
	tail    string
}

func newStream(dst io.Writer, values []string, observe func([]byte)) *stream {
	aliases := append([]string{}, values...)
	for _, v := range values {
		b, _ := json.Marshal(v)
		if len(b) >= 2 && string(b[1:len(b)-1]) != v {
			aliases = append(aliases, string(b[1:len(b)-1]))
		}
		var plain bytes.Buffer
		enc := json.NewEncoder(&plain)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		b = bytes.TrimSpace(plain.Bytes())
		if len(b) >= 2 && string(b[1:len(b)-1]) != v {
			aliases = append(aliases, string(b[1:len(b)-1]))
		}
	}
	return &stream{dst: dst, values: aliases, observe: observe}
}
func (s *stream) Write(b []byte) (int, error) {
	if s.observe != nil {
		for _, c := range b {
			if c == '\n' {
				if !s.discard {
					s.observe(s.line)
				}
				s.line = nil
				s.discard = false
			} else if !s.discard {
				if len(s.line) >= 2<<20 {
					s.discard = true
					s.line = nil
				} else {
					s.line = append(s.line, c)
				}
			}
		}
	}
	s.tail += string(b)
	if len(s.tail) > 64<<10 {
		s.tail = s.tail[len(s.tail)-(64<<10):]
	}
	s.pending = append(s.pending, b...)
	// 仅保留可能成为密钥的未完成后缀；完整 JSONL 必须立即转发，
	// 否则 app-server 的双向 RPC 会在等待最后换行时死锁。
	cut := len(s.pending)
	for _, value := range s.values {
		max := len(value) - 1
		if max > len(s.pending) {
			max = len(s.pending)
		}
		for n := max; n > 0; n-- {
			if bytes.Equal(s.pending[len(s.pending)-n:], []byte(value[:n])) {
				if at := len(s.pending) - n; at < cut {
					cut = at
				}
				break
			}
		}
	}
	if cut <= 0 {
		return len(b), nil
	}
	// 若密钥跨越切点，延后到密钥开始处，防止部分密钥先输出。
	for _, v := range s.values {
		if v == "" {
			continue
		}
		start := 0
		for {
			at := bytes.Index(s.pending[start:], []byte(v))
			if at < 0 {
				break
			}
			at += start
			if at < cut && at+len(v) > cut {
				cut = at
			}
			start = at + len(v)
			if start >= len(s.pending) {
				break
			}
		}
	}
	_, e := io.WriteString(s.dst, secrets.Redact(string(s.pending[:cut]), s.values))
	s.pending = append(s.pending[:0], s.pending[cut:]...)
	return len(b), e
}
func (s *stream) Close() error {
	if s.observe != nil && !s.discard && len(s.line) > 0 {
		s.observe(s.line)
	}
	_, e := io.WriteString(s.dst, secrets.Redact(string(s.pending), s.values))
	s.pending = nil
	return e
}
