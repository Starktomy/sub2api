package upstream_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstream"
)

type selector struct{}

func (selector) SelectProtocol(upstream.RouteHints) upstream.Protocol { return upstream.Responses }

func TestRegistrySnapshot(t *testing.T) {
	providers := []upstream.Provider{{ID: "z", Selector: selector{}}, {ID: "a", Selector: selector{}}}
	r, err := upstream.NewRegistry(providers...)
	if err != nil {
		t.Fatal(err)
	}
	providers[0].ID = "changed"
	ids := r.IDs()
	if !reflect.DeepEqual(ids, []string{"a", "z"}) {
		t.Fatal(ids)
	}
	ids[0] = "changed"
	if r.IDs()[0] != "a" {
		t.Fatal("mutable IDs")
	}
	p, err := r.Resolve("", "z")
	if err != nil || p.ID != "z" {
		t.Fatal(p, err)
	}
	p.Capabilities.Streaming = upstream.Supported
	again, _ := r.Resolve("z", "a")
	if again.Capabilities.Streaming != upstream.Unknown {
		t.Fatal("mutable metadata")
	}
	if _, err := r.Resolve("missing", "z"); err == nil {
		t.Fatal("explicit ID fell back")
	}
	if _, err := r.Resolve("", "missing"); err == nil {
		t.Fatal("missing default accepted")
	}
}

func TestRegistryConcurrentReads(t *testing.T) {
	r, err := upstream.NewRegistry(upstream.Provider{ID: "a", Selector: selector{}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				p, err := r.Resolve("a", "")
				if err != nil || p.Selector.SelectProtocol(upstream.RouteHints{}) != upstream.Responses {
					t.Error("inconsistent registry read")
				}
				r.IDs()[0] = "local mutation"
			}
		}()
	}
	wg.Wait()
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	for _, providers := range [][]upstream.Provider{
		{{ID: "a", Selector: selector{}}, {ID: "a", Selector: selector{}}},
		{{Selector: selector{}}}, {{ID: "a"}},
	} {
		if _, err := upstream.NewRegistry(providers...); err == nil {
			t.Fatal("accepted invalid registration")
		}
	}
}
