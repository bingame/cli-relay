-- CC Switch SQLite 导出
-- 本文件只使用虚构凭据，用于验证列顺序变化和批量 INSERT。
PRAGMA foreign_keys=OFF;
PRAGMA user_version=18;
BEGIN TRANSACTION;
CREATE TABLE providers (
  name TEXT NOT NULL,
  meta TEXT NOT NULL DEFAULT '{}',
  id TEXT NOT NULL,
  settings_config TEXT NOT NULL,
  is_current BOOLEAN NOT NULL DEFAULT 0,
  app_type TEXT NOT NULL,
  future_column TEXT,
  PRIMARY KEY (id, app_type)
);
INSERT INTO providers (name, meta, id, settings_config, is_current, app_type, future_column) VALUES
('Claude ''演示''; ATTACH', '{"credential":"fake-meta-secret"}', 'demo', '{"env":{"ANTHROPIC_AUTH_TOKEN":"fake-claude-key","ANTHROPIC_API_KEY":"fake-claude-secondary","ANTHROPIC_BASE_URL":"https://claude.example.test","ANTHROPIC_MODEL":"claude-example","OTHER_SECRET":"fake-extra-secret","SAFE_FLAG":"yes"},"permissions":{"allow":["Read"]},"hooks":{},"model":"default"}', 1, 'claude', '保留兼容'),
('Codex 演示', '{}', 'demo', '{"auth":{"OPENAI_API_KEY":"fake-codex-key"},"config":"model_provider = \"custom\"\nmodel = \"gpt-example\"\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://codex.example.test/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"fake-old-token\"\nhttp_headers = { X-Private = \"fake-header-secret\" }\nenv_http_headers = { X-Private = \"MY_API_KEY\" }\n[model_providers.unused]\nbase_url = \"https://unused.example.test\"","env":{"MY_API_KEY":"fake-env-key"}}', 0, 'codex', NULL),
('未来目标', '{"token":"fake-other-meta"}', '../无效 ID', '{"api_key":"fake-other-key","arbitrary":{"any":"data"}}', 0, 'gemini', NULL);
CREATE TABLE mcp_servers (id TEXT PRIMARY KEY, server_config TEXT);
INSERT INTO mcp_servers VALUES ('mcp-test', '{"env":{"TOKEN":"fake-mcp-secret"},"description":"O''Brien; --ATTACH"}');
CREATE TABLE prompts (id TEXT PRIMARY KEY, content TEXT, blob_value BLOB);
INSERT INTO prompts VALUES ('prompt-test', '多行
提示正文', X'0001FE');
CREATE INDEX provider_names ON providers(name);
COMMIT;
PRAGMA foreign_keys=ON;
