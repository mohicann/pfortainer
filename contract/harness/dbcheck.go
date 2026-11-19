package harness

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Rows: SELECT 결과를 []map으로. 값은 int64/float64/string/nil.
func Rows(t testing.TB, db *sql.DB, q string, args ...any) []map[string]any {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out
}

// MaskNow: 지금 시각 근처(±10분)의 unix 초를 "<now>"로 바꾼다.
func MaskNow(rows []map[string]any, cols ...string) []map[string]any {
	now := time.Now().Unix()
	for _, r := range rows {
		for _, c := range cols {
			if v, ok := r[c].(int64); ok && v > now-600 && v < now+600 {
				r[c] = "<now>"
			}
		}
	}
	return rows
}

// MaskBcrypt: password_hash가 bcrypt면 "<bcrypt>"(비용은 남김).
func MaskBcrypt(rows []map[string]any) []map[string]any {
	for _, r := range rows {
		if s, ok := r["password_hash"].(string); ok && strings.HasPrefix(s, "$2") && len(s) == 60 {
			r["password_hash"] = fmt.Sprintf("<bcrypt %s>", s[:7])
		}
	}
	return rows
}

// Users: users 테이블 스냅샷(해시·시각 마스킹).
func (a *App) Users(t testing.TB) []map[string]any {
	t.Helper()
	rows := Rows(t, a.DB(t), `SELECT id, username, password_hash, role, typeof(role) AS role_type, created_at, totp_secret != '' AS has_totp_secret, totp_enabled FROM users ORDER BY id`)
	return MaskNow(MaskBcrypt(rows), "created_at")
}

// Rows1: 첫 행 첫 칸 문자열.
func (a *App) Rows1(t testing.TB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := a.DB(t).QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	return s
}
