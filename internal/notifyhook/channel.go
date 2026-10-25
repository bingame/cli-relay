package notifyhook

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/bingame/cli-relay/internal/secrets"
)

// Channel 是用户配置的通知通道；Token 属于敏感值，只封存在加密文件里，
// 不进 argv、日志或普通配置文件。
type Channel struct {
	Type     string `json:"type"`     // webhook | bark | telegram | ntfy
	Endpoint string `json:"endpoint"` // webhook URL；bark/ntfy/telegram 服务器地址（可空用默认）
	Token    string `json:"token"`    // bark 设备 key / telegram bot token / ntfy 访问令牌 / webhook Bearer
	ChatID   string `json:"chat_id"`  // telegram
	Topic    string `json:"topic"`    // ntfy
}

const channelContext = "notify-channel"

type channelFile struct {
	Version int    `json:"version"`
	Data    string `json:"data"`
}

func channelPath(relayHome string) string {
	return filepath.Join(relayHome, "notify-channel.json")
}

// SaveChannel 将通道配置整体封存写入 relay 数据目录。
func SaveChannel(relayHome string, v *secrets.Vault, ch Channel) error {
	if err := validateChannel(ch); err != nil {
		return err
	}
	data, err := json.Marshal(ch)
	if err != nil {
		return fmt.Errorf("无法编码通知通道")
	}
	wrapper := channelFile{Version: 1, Data: base64.StdEncoding.EncodeToString(v.Seal(data, channelContext))}
	content, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码通知通道")
	}
	return safeio.WriteFile(channelPath(relayHome), append(content, '\n'), 0600)
}

// LoadChannel 读取通道配置；未配置时返回 ok=false。
func LoadChannel(relayHome string, v *secrets.Vault) (ch Channel, ok bool, err error) {
	raw, err := safeio.ReadRegular(channelPath(relayHome), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return Channel{}, false, nil
	}
	if err != nil {
		return Channel{}, false, err
	}
	var wrapper channelFile
	if json.Unmarshal(raw, &wrapper) != nil || wrapper.Version != 1 {
		return Channel{}, false, fmt.Errorf("通知通道记录无效")
	}
	sealed, err := base64.StdEncoding.DecodeString(wrapper.Data)
	if err != nil {
		return Channel{}, false, fmt.Errorf("通知通道记录无效")
	}
	plain, err := v.Open(sealed, channelContext)
	if err != nil {
		return Channel{}, false, err
	}
	if json.Unmarshal(plain, &ch) != nil {
		return Channel{}, false, fmt.Errorf("通知通道记录无效")
	}
	return ch, true, nil
}

// ClearChannel 删除通道配置；未配置时返回 false。
func ClearChannel(relayHome string) (bool, error) {
	if err := safeio.CheckPath(channelPath(relayHome)); err != nil {
		return false, err
	}
	if err := os.Remove(channelPath(relayHome)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ChannelExists 仅检查通道文件是否存在，不解锁内容。
func ChannelExists(relayHome string) bool {
	if err := safeio.CheckPath(channelPath(relayHome)); err != nil {
		return false
	}
	_, err := os.Stat(channelPath(relayHome))
	return err == nil
}

func validateChannel(ch Channel) error {
	switch ch.Type {
	case "webhook":
		if ch.Endpoint == "" {
			return fmt.Errorf("webhook 通道需要 URL")
		}
	case "bark":
		if ch.Token == "" {
			return fmt.Errorf("bark 通道需要设备 key")
		}
	case "telegram":
		if ch.Token == "" || ch.ChatID == "" {
			return fmt.Errorf("telegram 通道需要 bot token 与 chat id")
		}
	case "ntfy":
		if ch.Topic == "" {
			return fmt.Errorf("ntfy 通道需要 topic")
		}
	default:
		return fmt.Errorf("未知通知通道类型: %s", ch.Type)
	}
	return nil
}
