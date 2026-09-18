package provider

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bingame/cli-relay/internal/safeio"
	_ "modernc.org/sqlite"
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
	Models   []Model
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
 CREATE TABLE IF NOT EXISTS providers (id TEXT PRIMARY KEY,display_name TEXT NOT NULL,targets TEXT NOT NULL,base_url TEXT,model TEXT,extra_json TEXT,source TEXT,created_at TEXT,updated_at TEXT,secret_mode TEXT NOT NULL DEFAULT 'callback',status TEXT NOT NULL DEFAULT 'active',content_hash TEXT);
 CREATE TABLE IF NOT EXISTS provider_models(provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,model_id TEXT NOT NULL,display_name TEXT,context_window INTEGER,is_default INTEGER NOT NULL DEFAULT 0,sort_order INTEGER,PRIMARY KEY(provider_id,model_id));
 CREATE TABLE IF NOT EXISTS provider_secrets(provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,key_name TEXT NOT NULL,ciphertext BLOB NOT NULL,PRIMARY KEY(provider_id,key_name));
 CREATE TABLE IF NOT EXISTS import_log(id INTEGER PRIMARY KEY AUTOINCREMENT,source TEXT,file_hash TEXT,imported_at TEXT,raw_snapshot BLOB,seen_provider_ids TEXT);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	for _, migration := range []string{
		`ALTER TABLE providers ADD COLUMN secret_mode TEXT NOT NULL DEFAULT 'callback'`,
		`ALTER TABLE providers ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,
		`ALTER TABLE providers ADD COLUMN content_hash TEXT`,
		`ALTER TABLE import_log ADD COLUMN seen_provider_ids TEXT`,
	} {
		if _, err := db.Exec(migration); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db, cipher: cipher}, nil
}
func (s *Store) Close() error       { return s.db.Close() }
func (s *Store) SetCipher(c Cipher) { s.cipher = c }
func (s *Store) Add(ctx context.Context, p Provider, sec map[string]string) error {
	return s.Import(ctx, []Entry{{Provider: p, Secrets: sec}}, "", nil)
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
		p.Status = p.EffectiveStatus()
		p.SecretMode = p.EffectiveSecretMode()
		p.ContentHash = ContentHash(p, entry.Models)
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
		_, e = tx.ExecContext(ctx, `INSERT INTO providers(id,display_name,targets,base_url,model,extra_json,source,created_at,updated_at,secret_mode,status,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.DisplayName, string(targets), p.BaseURL, p.Model, string(extra), p.Source, p.CreatedAt, p.UpdatedAt, p.SecretMode, p.Status, p.ContentHash)
		if e != nil {
			return fmt.Errorf("无法保存供应商 %s（ID 可能重复）", p.ID)
		}
		for _, model := range entry.Models {
			if model.ProviderID == "" {
				model.ProviderID = p.ID
			}
			if model.ProviderID != p.ID || strings.TrimSpace(model.ModelID) == "" {
				return fmt.Errorf("供应商 %s 的模型目录无效", p.ID)
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO provider_models(provider_id,model_id,display_name,context_window,is_default,sort_order) VALUES(?,?,?,?,?,?)`, model.ProviderID, model.ModelID, model.DisplayName, model.ContextWindow, model.IsDefault, model.SortOrder)
			if e != nil {
				return fmt.Errorf("保存模型目录失败")
			}
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
	rows, e := s.db.QueryContext(ctx, `SELECT id,display_name,targets,base_url,model,extra_json,source,created_at,updated_at,secret_mode,status,content_hash FROM providers ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Provider{}
	for rows.Next() {
		var p Provider
		var targets, extra string
		if e = rows.Scan(&p.ID, &p.DisplayName, &targets, &p.BaseURL, &p.Model, &extra, &p.Source, &p.CreatedAt, &p.UpdatedAt, &p.SecretMode, &p.Status, &p.ContentHash); e != nil {
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

func (s *Store) Models(ctx context.Context, id string) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,model_id,display_name,context_window,is_default,sort_order FROM provider_models WHERE provider_id=? ORDER BY sort_order,model_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []Model{}
	for rows.Next() {
		var model Model
		if err = rows.Scan(&model.ProviderID, &model.ModelID, &model.DisplayName, &model.ContextWindow, &model.IsDefault, &model.SortOrder); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func (s *Store) Upsert(ctx context.Context, entry Entry) error {
	if err := entry.Provider.Validate(); err != nil {
		return err
	}
	if len(entry.Secrets) > 0 && s.cipher == nil {
		return fmt.Errorf("密钥库未解锁")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p := entry.Provider
	p.Status = p.EffectiveStatus()
	p.SecretMode = p.EffectiveSecretMode()
	p.ContentHash = ContentHash(p, entry.Models)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	targets, _ := json.Marshal(p.Targets)
	extra, err := json.Marshal(p.Extra)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO providers(id,display_name,targets,base_url,model,extra_json,source,created_at,updated_at,secret_mode,status,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,targets=excluded.targets,base_url=excluded.base_url,model=excluded.model,extra_json=excluded.extra_json,source=excluded.source,updated_at=excluded.updated_at,secret_mode=excluded.secret_mode,status=excluded.status,content_hash=excluded.content_hash`, p.ID, p.DisplayName, string(targets), p.BaseURL, p.Model, string(extra), p.Source, p.CreatedAt, p.UpdatedAt, p.SecretMode, p.Status, p.ContentHash)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM provider_models WHERE provider_id=?`, p.ID); err != nil {
		return err
	}
	for _, model := range entry.Models {
		model.ProviderID = p.ID
		if strings.TrimSpace(model.ModelID) == "" {
			return fmt.Errorf("模型 ID 不能为空")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO provider_models VALUES(?,?,?,?,?,?)`, p.ID, model.ModelID, model.DisplayName, model.ContextWindow, model.IsDefault, model.SortOrder); err != nil {
			return err
		}
	}
	if len(entry.Secrets) > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM provider_secrets WHERE provider_id=?`, p.ID); err != nil {
			return err
		}
		for k, v := range entry.Secrets {
			if _, err = tx.ExecContext(ctx, `INSERT INTO provider_secrets VALUES(?,?,?)`, p.ID, k, s.cipher.Seal([]byte(v), p.ID+":"+k)); err != nil {
				return fmt.Errorf("保存加密凭据失败")
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SetDisabledExcept(ctx context.Context, seen []string) ([]string, error) {
	seenSet := map[string]bool{}
	for _, id := range seen {
		seenSet[id] = true
	}
	items, err := s.List(ctx, "")
	if err != nil {
		return nil, err
	}
	disabled := []string{}
	for _, p := range items {
		if p.Source == "cc-switch-import" && !seenSet[p.ID] && p.EffectiveStatus() != "disabled" {
			if _, err = s.db.ExecContext(ctx, `UPDATE providers SET status='disabled',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), p.ID); err != nil {
				return nil, err
			}
			disabled = append(disabled, p.ID)
		}
	}
	return disabled, nil
}

func (s *Store) RecordImport(ctx context.Context, hash string, snapshot []byte, seen []string) error {
	if len(snapshot) > 0 && s.cipher == nil {
		return fmt.Errorf("密钥库未解锁")
	}
	var sealed []byte
	if len(snapshot) > 0 {
		sealed = s.cipher.Seal(snapshot, "import:"+hash)
	}
	ids, _ := json.Marshal(seen)
	_, err := s.db.ExecContext(ctx, `INSERT INTO import_log(source,file_hash,imported_at,raw_snapshot,seen_provider_ids) VALUES('cc-switch',?,?,?,?)`, hash, time.Now().UTC().Format(time.RFC3339Nano), sealed, string(ids))
	return err
}

func (s *Store) HardPrune(ctx context.Context) (int64, error) {
	r, e := s.db.ExecContext(ctx, `DELETE FROM providers WHERE status='disabled'`)
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
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
