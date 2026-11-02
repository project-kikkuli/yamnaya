package usage

import (
	"database/sql"
	"encoding/json"
	"path/filepath"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func KnownExhaustions(dir string) ([]core.Exhaustion, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ym.db")+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT body FROM ym_usage_exhaustions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Exhaustion
	for rows.Next() {
		var body string
		var e core.Exhaustion
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
