//go:build live

// Живая проверка на настоящем YouTube: go test -tags live ./internal/transcript/
// В обычный прогон и CI не входит — зависит от сети и настроения YouTube.
package transcript

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

type countingTransport struct{ n atomic.Int32 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

func TestLive(t *testing.T) {
	cases := []struct {
		id, lang string
		auto     bool
	}{
		{"dQw4w9WgXcQ", "en", false}, // ручные субтитры и ещё 5 языков
		{"jNQXAC9IVRw", "en", false}, // первое видео YouTube
	}
	for _, c := range cases {
		rt := &countingTransport{}
		cl := New(Options{HTTP: &http.Client{Transport: rt}})
		tr, err := cl.Fetch(context.Background(), c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		t.Logf("%s: %s auto=%v, %d строк, запросов %d; первая: %q", c.id, tr.Lang, tr.Auto, len(tr.Lines), rt.n.Load(), tr.Lines[0].Text)
		if tr.Lang != c.lang || len(tr.Lines) < 3 {
			t.Errorf("%s: язык %s, строк %d", c.id, tr.Lang, len(tr.Lines))
		}
		if rt.n.Load() != 2 {
			t.Errorf("%s: %d запросов, ожидалось 2", c.id, rt.n.Load())
		}
	}
}
