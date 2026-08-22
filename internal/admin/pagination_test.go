package admin

import (
	"net/http/httptest"
	"testing"
)

func TestParsePage(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"missing defaults to 1", "", 1},
		{"invalid non-numeric defaults to 1", "?page=abc", 1},
		{"negative defaults to 1", "?page=-1", 1},
		{"zero defaults to 1", "?page=0", 1},
		{"valid value read", "?page=3", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/images"+tt.query, nil)
			if got := parsePage(r); got != tt.want {
				t.Errorf("parsePage() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPageURL(t *testing.T) {
	t.Run("page 1 omits query param", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images?page=5", nil)
		got := pageURL(r, 1)
		if want := "/images"; got != want {
			t.Errorf("pageURL() = %q, want %q", got, want)
		}
	})

	t.Run("preserves other query params", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images?node=abc&page=1", nil)
		got := pageURL(r, 2)
		if want := "/images?node=abc&page=2"; got != want {
			t.Errorf("pageURL() = %q, want %q", got, want)
		}
	})

	t.Run("page above 1 sets query param", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/volumes", nil)
		got := pageURL(r, 4)
		if want := "/volumes?page=4"; got != want {
			t.Errorf("pageURL() = %q, want %q", got, want)
		}
	})
}

func TestNewPagination(t *testing.T) {
	t.Run("clamps page above TotalPages", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images", nil)
		p := newPagination(r, 99, 120) // 120 items / 50 per page = 3 pages
		if p.TotalPages != 3 {
			t.Fatalf("TotalPages = %d, want 3", p.TotalPages)
		}
		if p.Page != 3 {
			t.Fatalf("Page = %d, want clamped to 3", p.Page)
		}
		if !p.HasPrev || p.HasNext {
			t.Fatalf("HasPrev=%v HasNext=%v, want true/false at last page", p.HasPrev, p.HasNext)
		}
	})

	t.Run("first page", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images", nil)
		p := newPagination(r, 1, 120)
		if p.HasPrev {
			t.Fatal("HasPrev = true on first page")
		}
		if !p.HasNext {
			t.Fatal("HasNext = false on first page with more pages")
		}
	})

	t.Run("middle page", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images", nil)
		p := newPagination(r, 2, 120)
		if !p.HasPrev || !p.HasNext {
			t.Fatalf("HasPrev=%v HasNext=%v, want true/true on middle page", p.HasPrev, p.HasNext)
		}
	})

	t.Run("zero total still yields 1 page", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/images", nil)
		p := newPagination(r, 1, 0)
		if p.TotalPages != 1 {
			t.Fatalf("TotalPages = %d, want 1", p.TotalPages)
		}
		if p.HasPrev || p.HasNext {
			t.Fatalf("HasPrev=%v HasNext=%v, want false/false with no items", p.HasPrev, p.HasNext)
		}
	})
}

func TestPaginateSlice(t *testing.T) {
	items := make([]int, 120)
	for i := range items {
		items[i] = i
	}

	t.Run("page 1", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?page=1", nil)
		got, p := paginateSlice(r, items)
		if len(got) != 50 {
			t.Fatalf("len = %d, want 50", len(got))
		}
		if got[0] != 0 || got[len(got)-1] != 49 {
			t.Fatalf("page 1 window = [%d..%d], want [0..49]", got[0], got[len(got)-1])
		}
		if p.Page != 1 || p.TotalPages != 3 {
			t.Fatalf("Page=%d TotalPages=%d, want 1/3", p.Page, p.TotalPages)
		}
	})

	t.Run("page 2", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?page=2", nil)
		got, _ := paginateSlice(r, items)
		if len(got) != 50 {
			t.Fatalf("len = %d, want 50", len(got))
		}
		if got[0] != 50 || got[len(got)-1] != 99 {
			t.Fatalf("page 2 window = [%d..%d], want [50..99]", got[0], got[len(got)-1])
		}
	})

	t.Run("page 3 partial", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?page=3", nil)
		got, _ := paginateSlice(r, items)
		if len(got) != 20 {
			t.Fatalf("len = %d, want 20", len(got))
		}
		if got[0] != 100 || got[len(got)-1] != 119 {
			t.Fatalf("page 3 window = [%d..%d], want [100..119]", got[0], got[len(got)-1])
		}
	})

	t.Run("page beyond range clamps to last page", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x?page=99", nil)
		got, p := paginateSlice(r, items)
		if len(got) != 20 {
			t.Fatalf("len = %d, want 20", len(got))
		}
		if p.Page != 3 {
			t.Fatalf("Page = %d, want clamped to 3", p.Page)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/x", nil)
		got, p := paginateSlice(r, []int{})
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
		if p.TotalPages != 1 {
			t.Fatalf("TotalPages = %d, want 1", p.TotalPages)
		}
	})
}
