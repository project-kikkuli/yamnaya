package profile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func Identity(p Profile) string {
	if p.ID == "default" && !p.SubscriptionSafe("") {
		return ""
	}
	if p.Harness == "claude" {
		path := filepath.Join(p.Root, ".claude.json")
		if p.ID == "default" && !p.Explicit {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, ".claude.json")
		}
		if _, err := os.Stat(filepath.Join(p.Root, ".config.json")); err == nil {
			path = filepath.Join(p.Root, ".config.json")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		var d struct {
			Account struct {
				User string `json:"accountUuid"`
				Org  string `json:"organizationUuid"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(b, &d) != nil || d.Account.User == "" || d.Account.Org == "" {
			return ""
		}
		return "claude/" + d.Account.User + "/" + d.Account.Org
	}
	b, err := os.ReadFile(filepath.Join(p.Root, "auth.json"))
	if err != nil {
		return ""
	}
	var d struct {
		Tokens struct {
			ID      string `json:"id_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	parts := strings.Split(d.Tokens.ID, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			User     string `json:"chatgpt_user_id"`
			Fallback string `json:"user_id"`
			Account  string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	account := d.Tokens.Account
	if account == "" {
		account = claims.Auth.Account
	}
	if claims.Auth.User == "" {
		claims.Auth.User = claims.Auth.Fallback
	}
	if claims.Auth.User == "" || account == "" {
		return ""
	}
	return "codex/" + claims.Auth.User + "/" + account
}
