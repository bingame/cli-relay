package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/bingame/cli-relay/internal/safeio"
)

type cacheMetadata struct {
	SourceHash  string `json:"source_hash"`
	ContentHash string `json:"content_hash"`
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// WriteRendered 只自动替换陈旧的 Relay 产物；同一来源下的手工修改必须显式处理。
func WriteRendered(path string, content []byte, sourceHash string) error {
	metaPath := path + ".relay-cache.json"
	existing, readErr := safeio.ReadRegular(path, 32<<20)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	metaData, metaErr := safeio.ReadRegular(metaPath, 1<<20)
	if metaErr == nil && readErr == nil {
		var meta cacheMetadata
		if json.Unmarshal(metaData, &meta) == nil && meta.ContentHash != contentHash(existing) {
			return fmt.Errorf("渲染产物已被手工修改，拒绝静默覆盖: %s", path)
		}
	}
	if readErr == nil && string(existing) == string(content) {
		return nil
	}
	if err := safeio.WriteFile(path, content, 0600); err != nil {
		return err
	}
	meta := cacheMetadata{SourceHash: sourceHash, ContentHash: contentHash(content)}
	encoded, _ := json.Marshal(meta)
	return safeio.WriteFile(metaPath, append(encoded, '\n'), 0600)
}

// RemoveRendered 清理某个渲染产物及其缓存元数据。用于产物形态变化后不再需要
// 该文件的场景（如供应商不再声明模型目录，见 codex.Adapter.Render）；
// 文件本就不存在不算失败。
func RemoveRendered(path string) error {
	var first error
	for _, candidate := range []string{path, path + ".relay-cache.json"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}
