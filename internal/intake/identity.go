package intake

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func Identity(kind, address string) (string, error) {
	if kind == "file" {
		path := strings.TrimPrefix(address, "file://")
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return "file://" + absolute, nil
	}
	u, err := url.Parse(address)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("source needs an https address")
	}
	switch kind {
	case "github":
		if u.Host != "github.com" {
			return "", fmt.Errorf("expected github.com")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 4 || parts[2] != "pull" {
			return "", fmt.Errorf("expected a pull request address")
		}
		return "https://github.com/" + strings.Join(parts, "/"), nil
	case "notion":
		if u.Host != "notion.so" && u.Host != "www.notion.so" && !strings.HasSuffix(u.Host, ".notion.site") {
			return "", fmt.Errorf("expected a Notion page")
		}
		id := notionID.FindString(u.Path)
		if id == "" {
			return "", fmt.Errorf("Notion page has no identity")
		}
		return "https://www.notion.so/" + strings.ToLower(strings.ReplaceAll(id, "-", "")), nil
	}
	return "", fmt.Errorf("unsupported source kind %q", kind)
}
