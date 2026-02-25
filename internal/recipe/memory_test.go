package recipe

import (
	"context"
	"testing"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

func TestMemorySourceList(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	src := NewMemorySource(log)
	ctx := context.Background()

	recipes, err := src.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recipes) < 2 {
		t.Fatalf("expected at least 2 recipes, got %d", len(recipes))
	}
}

func TestMemorySourceGet(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	src := NewMemorySource(log)
	ctx := context.Background()

	tests := []struct {
		id      string
		wantErr error
	}{
		{"chicken-alfredo", nil},
		{"vegetable-stir-fry", nil},
		{"nonexistent", domain.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			r, err := src.Get(ctx, tt.id)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("expected %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.ID != tt.id {
				t.Fatalf("expected ID %s, got %s", tt.id, r.ID)
			}
			if len(r.Steps) == 0 {
				t.Fatal("recipe has no steps")
			}
			if len(r.Ingredients) == 0 {
				t.Fatal("recipe has no ingredients")
			}
		})
	}
}

func TestMemorySourceSearch(t *testing.T) {
	log := logger.New(logger.LevelOff, nil)
	src := NewMemorySource(log)
	ctx := context.Background()

	tests := []struct {
		query    string
		minCount int
	}{
		{"chicken", 1},
		{"pasta", 1},
		{"vegan", 1},
		{"nonexistent-query-xyz", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			results, err := src.Search(ctx, tt.query)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(results) < tt.minCount {
				t.Fatalf("query=%q: expected at least %d results, got %d", tt.query, tt.minCount, len(results))
			}
		})
	}
}
