package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"relay/internal/safeio"
)

type Cipher interface {
	Seal([]byte, string) []byte
	Open([]byte, string) ([]byte, error)
}
type Store struct {
	db     *sql.DB
	cipher Cipher
}
type Entry struct {
	Provider Provider
	Secrets  map[string]string
}

func OpenReadOnly(root string) (*Store, error) {
	path, e := filepath.Abs(filepath.Join(root, "providers.db"))
	if e != nil {
		return nil, e
	}
	if e = safeio.CheckPath(path); e != nil {
		return nil, e
	}
	uriPath := filepath.ToSlash(path)
	if len(uriPath) > 1 && uriPath[1] == ':' {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func Open(root string, cipher Cipher) (*Store, error) {
	if err := safeio.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "providers.db")
	if e := safeio.CheckPath(path); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY,display_name TEXT NOT NULL,targets TEXT NOT NULL,base_url TEXT,model TEXT,extra_json TEXT,source TEXT,created_at TEXT,updated_at TEXT);
 CREATE TABLE IF NOT EXISTS provider_secrets(provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,key_name TEXT NOT NULL,ciphertext BLOB NOT NULL,PRIMARY KEY(provider_id,key_name));
 CREATE TABLE IF NOT EXISTS import_log(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT,file_hash TEXT,imported_at TEXT,raw_snapshot BLOB);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db: db, cipher: cipher}, nil
}
func (s *Store) Close() error       { return s.db.Close() }
func (s *Store) SetCipher(c Cipher) { s.cipher = c }
func (s *Store) Add(ctx context.Context, p Provider, sec map[string]string) error {
	return s.Import(ctx, []Entry{{p, sec}}, "", nil)
}
func (s *Store) Import(ctx context.Context, entries []Entry, hash string, snapshot []byte) error {
	for _, e := range entries {
		if err := e.Provider.Validate(); err != nil {
			return err
		}
		if len(e.Secrets) > 0 && s.cipher == nil {
			return fmt.Errorf("密钥库未解锁")
		}
	}
	if len(snapshot) > 0 && s.cipher == nil {
		return fmt.Errorf("密钥库未解锁")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range entries {
		p := entry.Provider
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if p.CreatedAt == "" {
			p.CreatedAt = now
		}
		p.UpdatedAt = now
		targets, _ := json.Marshal(p.Targets)
		extra, e := json.Marshal(p.Extra)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO providers VALUES(?,?,?,?,?,?,?,?,?)`, p.ID, p.DisplayName, string(targets), p.BaseURL, p.Model, string(extra), p.Source, p.CreatedAt, p.UpdatedAt)
		if e != nil {
			return fmt.Errorf("无法保存供应商 %s（ID 可能重复）", p.ID)
		}
		for k, v := range entry.Secrets {
			_, e = tx.ExecContext(ctx, `INSERT INTO provider_secrets VALUES(?,?,?)`, p.ID, k, s.cipher.Seal([]byte(v), p.ID+":"+k))
			if e != nil {
				return fmt.Errorf("保存加密凭据失败")
			}
		}
	}
	if hash != "" {
		var sealed []byte
		if len(snapshot) > 0 {
			sealed = s.cipher.Seal(snapshot, "import:"+hash)
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO import_log(source,file_hash,imported_at,raw_snapshot) VALUES('cc-switch',?,?,?)`, hash, time.Now().UTC().Format(time.RFC3339Nano), sealed); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) List(ctx context.Context, target string) ([]Provider, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,display_name,targets,base_url,model,extra_json,source,created_at,updated_at FROM providers ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Provider{}
	for rows.Next() {
		var p Provider
		var targets, extra string
		if e = rows.Scan(&p.ID, &p.DisplayName, &targets, &p.BaseURL, &p.Model, &extra, &p.Source, &p.CreatedAt, &p.UpdatedAt); e != nil {
			return nil, e
		}
		if json.Unmarshal([]byte(targets), &p.Targets) != nil || json.Unmarshal([]byte(extra), &p.Extra) != nil {
			return nil, fmt.Errorf("供应商数据损坏")
		}
		if target == "" || p.Supports(target) {
			result = append(result, p)
		}
	}
	return result, rows.Err()
}
func (s *Store) Get(ctx context.Context, id string) (Provider, error) {
	list, e := s.List(ctx, "")
	if e != nil {
		return Provider{}, e
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("供应商不存在: %s", id)
}
func (s *Store) Secrets(ctx context.Context, id string) (map[string]string, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT key_name,ciphertext FROM provider_secrets WHERE provider_id=?`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var b []byte
		if e = rows.Scan(&k, &b); e != nil {
			return nil, e
		}
		if s.cipher == nil {
			return nil, fmt.Errorf("密钥库未解锁")
		}
		v, e := s.cipher.Open(b, id+":"+k)
		if e != nil {
			return nil, e
		}
		out[k] = string(v)
	}
	return out, rows.Err()
}
func (s *Store) Remove(ctx context.Context, id string) error {
	r, e := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id=?`, id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("供应商不存在: %s", id)
	}
	return nil
}
