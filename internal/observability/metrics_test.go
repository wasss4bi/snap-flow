package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"strings"
	"testing"
)

func TestSQLFingerprintIgnoresValues(t *testing.T) {
	a := SQLFingerprint("SELECT * FROM users WHERE email = 'person@example.test' AND id = 11")
	b := SQLFingerprint("SELECT * FROM users WHERE email = 'different@example.test' AND id = 42")
	if a != b || strings.Contains(a, "person") {
		t.Fatalf("queries not grouped safely: %q %q", a, b)
	}
	if a == SQLFingerprint("SELECT * FROM users WHERE email = 'person@example.test'") {
		t.Fatal("different queries grouped")
	}
}

func TestSQLTemplateProtectsLiterals(t *testing.T) {
	for _, query := range []string{
		"SELECT 'private@example.test'", "SELECT E'private\\'value'", "SELECT $$private$$",
		"SELECT $tag$private$tag$", "SELECT 1 -- private", "SELECT /* private */ 1",
	} {
		if got := SQLTemplate(query); strings.Contains(got, "private") {
			t.Fatalf("template leaked literal from %q: %q", query, got)
		}
	}
	got := SQLTemplate(`SELECT * FROM "users" WHERE id = $1 AND count > 42`)
	if got != `SELECT * FROM "users" WHERE id = $1 AND count > ?` {
		t.Fatalf("unexpected template %q", got)
	}
}
func TestExtremesResetEachScrape(t *testing.T) {
	e := newExtremes("test_duration", []string{"route"})
	e.observe(0.2, "/api")
	e.observe(0.7, "/api")
	e.observe(0.4, "/api")
	ch := make(chan prometheus.Metric, 2)
	e.Collect(ch)
	close(ch)
	var got []float64
	for m := range ch {
		var x dto.Metric
		if err := m.Write(&x); err != nil {
			t.Fatal(err)
		}
		got = append(got, x.GetGauge().GetValue())
	}
	if len(got) != 2 || got[0] != 0.2 || got[1] != 0.7 {
		t.Fatalf("got %v", got)
	}
	ch2 := make(chan prometheus.Metric, 2)
	e.Collect(ch2)
	if len(ch2) != 0 {
		t.Fatal("repeated previous interval")
	}
}
