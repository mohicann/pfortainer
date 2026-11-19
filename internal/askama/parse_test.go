package askama

import (
	"os"
	"path/filepath"
	"testing"
)

// 모든 Rust 템플릿이 파싱되는지.
func TestParseAll(t *testing.T) {
	files, _ := filepath.Glob("../pf/templates/*.html")
	if len(files) == 0 {
		t.Fatal("템플릿 없음")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if _, err := Parse(filepath.Base(f), string(b)); err != nil {
			t.Error(err)
		}
	}
}

func TestRenderBasic(t *testing.T) {
	tpl, err := Parse("x", `{% let a = n > 1 && s.is_empty() %}{% if let Some(m) = e %}[{{ m }}]{% endif %}{% for (i, v) in xs.iter().enumerate() %}{{ i }}={{ v }}{% if i > 0 %}!{% endif %},{% endfor %}{{ a }} {{ q }} {{ x|safe }} {{ n - 5 }} {{ xs.len() }}`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Set{cache: map[string]*Template{"x": tpl}}
	m := "<hi>"
	out, err := s.Render("x", map[string]any{"n": int64(3), "s": "", "e": &m, "xs": []string{"a", "b"}, "q": `"&'<>`, "x": "<b>"})
	if err != nil {
		t.Fatal(err)
	}
	want := `[&#60;hi&#62;]0=a,1=b!,true &#34;&#38;&#39;&#60;&#62; <b> -2 2`
	if out != want {
		t.Fatalf("got %q", out)
	}
}
