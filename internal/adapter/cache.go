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
