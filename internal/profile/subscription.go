package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var subscriptionOverrides = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_PROJECT_DIR_NAME",
	"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "OPENAI_BASE_URL",
	"CODEX_REFRESH_TOKEN_URL_OVERRIDE", "CODEX_REVOKE_TOKEN_URL_OVERRIDE", "CODEX_APP_SERVER_LOGIN_CLIENT_ID",
}

func (p Profile) ValidateSubscription(cwd string) error {
	if err := p.DefaultConstraint(); err != nil {
		return err
	}
	if p.ID == "default" {
		return nil
	}
	if p.Harness == "codex" {
		config, err := readCodexConfig(p.Root)
		if err != nil {
			return err
		}
		if config.APIKey != "" || config.APIKeyHelper != "" || config.OpenAIBaseURL != "" || config.ChatGPTBaseURL != "" || strings.Contains(config.ForcedLoginMethod, "api") || (config.ModelProvider != "" && config.ModelProvider != "openai") {
			return fmt.Errorf("profile %s config overrides subscription authentication", p.ID)
		}

		return nil
	}
	paths := []string{filepath.Join(p.Root, "settings.json")}
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var doc map[string]json.RawMessage
		if err = json.Unmarshal(b, &doc); err != nil {
			return err
		}
		var helper string
		if raw := doc["apiKeyHelper"]; len(raw) > 0 {
			if err = json.Unmarshal(raw, &helper); err != nil {
				return fmt.Errorf("profile %s has an invalid apiKeyHelper setting", p.ID)
			}
		}
		if helper != "" {
			return fmt.Errorf("profile %s uses apiKeyHelper instead of its subscription", p.ID)
		}
		var env map[string]string
		if raw := doc["env"]; len(raw) > 0 {
			if err = json.Unmarshal(raw, &env); err != nil {
				return fmt.Errorf("profile %s settings env must contain string values", p.ID)
			}
		}
		{
			for _, k := range subscriptionOverrides {
				if v, ok := env[k]; ok && v != "" {
					return fmt.Errorf("profile %s settings override subscription authentication (%s)", p.ID, k)
				}
			}
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
				if v, ok := env[k]; ok && v != p.Root {
					return fmt.Errorf("profile %s settings redirect %s", p.ID, k)
				}
			}
		}
	}
	return nil
}

func (p Profile) SubscriptionSafe(cwd string) bool {
	if p.DefaultConstraint() != nil {
		return false
	}
	if p.AuthOverride {
		return false
	}
	if p.ID == "default" {
		p.ID = "default-validation"
	}
	return p.ValidateSubscription(cwd) == nil
}
func authEnvironment(harness string) bool {
	for _, key := range subscriptionOverrides {
		matches := strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_")
		if harness == "codex" {
			matches = strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "CODEX_")
		}
		if matches && os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func (p Profile) DefaultConstraint() error {
	if p.Harness == "claude" && ID(p.ID) == "default" && p.RootSet && (p.RootValue == "" || !filepath.IsAbs(p.RootValue)) {
		return fmt.Errorf("default CLAUDE_CONFIG_DIR is empty or relative: installed Claude uses it verbatim, so its storage depends on the working directory; choose a named absolute profile, or intentionally unset CLAUDE_CONFIG_DIR or configure an absolute default")
	}
	return nil
}
