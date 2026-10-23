package profile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type codexConfig struct {
	SQLiteHome        string                 `toml:"sqlite_home"`
	ModelProvider     string                 `toml:"model_provider"`
	APIKey            string                 `toml:"api_key"`
	APIKeyHelper      string                 `toml:"api_key_helper"`
	OpenAIBaseURL     string                 `toml:"openai_base_url"`
	ChatGPTBaseURL    string                 `toml:"chatgpt_base_url"`
	ForcedLoginMethod string                 `toml:"forced_login_method"`
	Profile           string                 `toml:"profile"`
	Profiles          map[string]codexConfig `toml:"profiles"`
}

func readCodexConfig(root string) (codexConfig, error) {
	var config codexConfig
	b, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err = toml.Unmarshal(b, &config); err != nil {
		return config, fmt.Errorf("native config.toml is invalid")
	}
	if selected, ok := config.Profiles[config.Profile]; ok {
		if selected.SQLiteHome != "" {
			config.SQLiteHome = selected.SQLiteHome
		}
		if selected.ModelProvider != "" {
			config.ModelProvider = selected.ModelProvider
		}
		if selected.APIKey != "" {
			config.APIKey = selected.APIKey
		}
		if selected.APIKeyHelper != "" {
			config.APIKeyHelper = selected.APIKeyHelper
		}
		if selected.OpenAIBaseURL != "" {
			config.OpenAIBaseURL = selected.OpenAIBaseURL
		}
		if selected.ChatGPTBaseURL != "" {
			config.ChatGPTBaseURL = selected.ChatGPTBaseURL
		}
		if selected.ForcedLoginMethod != "" {
			config.ForcedLoginMethod = selected.ForcedLoginMethod
		}
	}
	return config, nil
}
