// Package recipe provides recipe source implementations.
package recipe

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.RecipeSource = (*MemorySource)(nil)

// MemorySource holds recipes in memory. Safe for concurrent reads.
type MemorySource struct {
	mu      sync.RWMutex
	recipes map[string]*domain.Recipe
	log     *logger.Logger
}

// NewMemorySource creates a recipe source preloaded with built-in recipes.
func NewMemorySource(log *logger.Logger) *MemorySource {
	src := &MemorySource{
		recipes: make(map[string]*domain.Recipe),
		log:     log,
	}
	src.seed()
	return src
}

// List returns summaries of all available recipes.
func (s *MemorySource) List(ctx context.Context) ([]domain.RecipeSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.log.Debug("listing all recipes, count=%d", len(s.recipes))

	out := make([]domain.RecipeSummary, 0, len(s.recipes))
	for _, r := range s.recipes {
		out = append(out, domain.RecipeSummary{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Tags:        r.Tags,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns a recipe by ID.
func (s *MemorySource) Get(ctx context.Context, id string) (*domain.Recipe, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.recipes[id]
	if !ok {
		s.log.Debug("recipe not found: %s", id)
		return nil, domain.ErrNotFound
	}
	return r, nil
}

// Update replaces a recipe in the source. The recipe ID must already exist.
func (s *MemorySource) Update(ctx context.Context, recipe *domain.Recipe) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.recipes[recipe.ID]; !ok {
		return domain.ErrNotFound
	}
	recipe.Version++
	s.recipes[recipe.ID] = recipe
	s.log.Info("recipe updated: %s (v%d)", recipe.Name, recipe.Version)
	return nil
}

// Search returns recipes whose name or tags contain the query string.
func (s *MemorySource) Search(ctx context.Context, query string) ([]domain.RecipeSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	q := strings.ToLower(query)
	s.log.Debug("searching recipes for: %s", q)

	var out []domain.RecipeSummary
	for _, r := range s.recipes {
		if s.matches(r, q) {
			out = append(out, domain.RecipeSummary{
				ID:          r.ID,
				Name:        r.Name,
				Description: r.Description,
				Tags:        r.Tags,
			})
		}
	}
	return out, nil
}

func (s *MemorySource) matches(r *domain.Recipe, query string) bool {
	if strings.Contains(strings.ToLower(r.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(r.Description), query) {
		return true
	}
	for _, tag := range r.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

// seed populates the source with built-in recipes.
func (s *MemorySource) seed() {
	recipes := []*domain.Recipe{
		s.vegetableStirFry(),
		s.chickenAlfredo(),
	}
	for _, r := range recipes {
		s.recipes[r.ID] = r
	}
	s.log.Debug("seeded %d recipes", len(recipes))
}

func (s *MemorySource) chickenAlfredo() *domain.Recipe {
	return &domain.Recipe{
		ID:          "chicken-alfredo",
		Name:        "Chicken Alfredo",
		Description: "Creamy spaghetti alfredo with pan-seared chicken. Rich, indulgent, and not from a jar.",
		Servings:    2,
		Tags:        []string{"italian", "pasta", "chicken", "comfort"},
		Ingredients: []domain.Ingredient{
			{Name: "spaghetti", Quantity: 250, Unit: "grams"},
			{Name: "chicken breast", Quantity: 2, Unit: "pieces", SizeDescriptor: "medium"},
			{Name: "creme fraiche", Quantity: 1, Unit: "cup"},
			{Name: "gruyere cheese", Quantity: 1, Unit: "cup", SizeDescriptor: "grated"},
			{Name: "margarine", Quantity: 3, Unit: "tablespoons"},
			{Name: "garlic", Quantity: 4, Unit: "cloves", SizeDescriptor: "medium"},
			{Name: "olive oil", Quantity: 1, Unit: "tablespoon"},
			{Name: "salt", Quantity: 0, Unit: "", SizeDescriptor: "to taste"},
			{Name: "black pepper", Quantity: 0, Unit: "", SizeDescriptor: "to taste"},
		},
		Steps: []domain.Step{
			{
				ID: "ca-1", Order: 1,
				Instruction: "Bring a large pot of salted water to a boil for the pasta. Don't be shy with the salt -- it should taste like the sea.",
				Duration:    8 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Water is at a rolling boil"},
				},
				TimerConfig: &domain.TimerConfig{Duration: 8 * time.Minute, Label: "Water boiling"},
			},
			{
				ID: "ca-2", Order: 2,
				Instruction:   "While the water heats, season the chicken breasts with salt and pepper on both sides. Pound them to even thickness if they're uneven -- otherwise the thin end dries out while the thick end is still raw.",
				ParallelHints: []string{"Do this while waiting for water to boil"},
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "Chicken is seasoned and even thickness"},
				},
			},
			{
				ID: "ca-3", Order: 3,
				Instruction: "Heat olive oil in a skillet over medium-high heat. Sear the chicken for about 6 minutes per side until golden and cooked through. Internal temp should hit 165 F. Set aside and let rest.",
				Duration:    12 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Chicken is golden brown on both sides, juices run clear"},
					{Type: domain.ConditionTemperature, Description: "Internal temperature reaches 165°F / 74°C"},
				},
				TimerConfig: &domain.TimerConfig{Duration: 12 * time.Minute, Label: "Chicken searing"},
			},
			{
				ID: "ca-4", Order: 4,
				Instruction: "Drop the spaghetti into the boiling water. Cook until al dente. Reserve a cup of pasta water before draining.",
				Duration:    10 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionTime, Description: "About 10 minutes or per package directions"},
				},
				TimerConfig: &domain.TimerConfig{Duration: 10 * time.Minute, Label: "Pasta cooking"},
			},
			{
				ID: "ca-5", Order: 5,
				Instruction: "In the same skillet, melt margarine over medium heat. Add minced garlic and cook for about 1 minute until fragrant. Do not burn it -- burnt garlic ruins everything.",
				Duration:    1 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Garlic is fragrant and lightly golden"},
				},
			},
			{
				ID: "ca-6", Order: 6,
				Instruction: "Stir in the creme fraiche. Bring to a gentle simmer and let it reduce for about 3 minutes, stirring occasionally. It should start to thicken slightly.",
				Duration:    3 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Cream has thickened slightly and coats the back of a spoon"},
				},
				TimerConfig: &domain.TimerConfig{Duration: 3 * time.Minute, Label: "Cream reducing"},
			},
			{
				ID: "ca-7", Order: 7,
				Instruction: "Take the pan off the heat. Stir in the gruyere gradually until melted and smooth. If it's too thick, splash in some of that reserved pasta water.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Sauce is smooth, creamy, and coats the pasta well"},
				},
			},
			{
				ID: "ca-8", Order: 8,
				Instruction: "Slice the rested chicken into strips. Toss the drained pasta into the sauce. Add the chicken on top. Serve immediately -- alfredo does not reheat well.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "Plated with chicken on top"},
				},
			},
		},
		Version: 1,
	}
}

func (s *MemorySource) vegetableStirFry() *domain.Recipe {
	return &domain.Recipe{
		ID:          "vegetable-stir-fry",
		Name:        "Vegetable Stir Fry",
		Description: "Fast, crunchy, and customizable. The key is a screaming hot pan and not overcrowding it.",
		Servings:    2,
		Tags:        []string{"asian", "vegetables", "quick", "vegan", "healthy"},
		Ingredients: []domain.Ingredient{
			{Name: "bell pepper", Quantity: 1, Unit: "pieces", SizeDescriptor: "large"},
			{Name: "broccoli florets", Quantity: 2, Unit: "cups"},
			{Name: "carrot", Quantity: 1, Unit: "pieces", SizeDescriptor: "medium"},
			{Name: "snap peas", Quantity: 1, Unit: "cup"},
			{Name: "garlic", Quantity: 3, Unit: "cloves", SizeDescriptor: "medium"},
			{Name: "fresh ginger", Quantity: 1, Unit: "tablespoon", SizeDescriptor: "grated"},
			{Name: "soy sauce", Quantity: 2, Unit: "tablespoons"},
			{Name: "sesame oil", Quantity: 1, Unit: "tablespoon"},
			{Name: "vegetable oil", Quantity: 2, Unit: "tablespoons"},
			{Name: "cornstarch", Quantity: 1, Unit: "teaspoon", Optional: true},
			{Name: "rice", Quantity: 1, Unit: "cup", Optional: true},
		},
		Steps: []domain.Step{
			{
				ID: "vsf-1", Order: 1,
				Instruction:   "If serving with rice, start the rice first. Get that going before you touch anything else.",
				ParallelHints: []string{"Rice cooks in the background while you prep and stir-fry"},
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "Rice is on, or skipped if not using rice"},
				},
			},
			{
				ID: "vsf-2", Order: 2,
				Instruction: "Prep all vegetables: slice the bell pepper into strips, cut broccoli into small florets, julienne the carrot, trim snap peas. Mince the garlic and grate the ginger. Everything cut BEFORE the pan goes on.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "All vegetables prepped and within arm's reach"},
				},
			},
			{
				ID: "vsf-3", Order: 3,
				Instruction: "Mix the sauce: soy sauce, sesame oil, and cornstarch (if using) with 2 tablespoons of water. Set aside.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "Sauce is mixed"},
				},
			},
			{
				ID: "vsf-4", Order: 4,
				Instruction: "Heat your wok or largest pan on HIGH heat until it just starts to smoke. Add vegetable oil and swirl to coat.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Pan is smoking slightly, oil is shimmering"},
				},
			},
			{
				ID: "vsf-5", Order: 5,
				Instruction: "Add broccoli and carrots first -- they take longest. Stir-fry for 2 minutes. Then add bell peppers and snap peas. Another 2 minutes. Do NOT stir constantly -- let things get some char.",
				Duration:    4 * time.Minute,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Vegetables are bright colored with some charred edges, still crunchy"},
					{Type: domain.ConditionTime, Description: "About 4 minutes total"},
				},
				TimerConfig: &domain.TimerConfig{Duration: 4 * time.Minute, Label: "Stir-fry cooking"},
			},
			{
				ID: "vsf-6", Order: 6,
				Instruction: "Push vegetables to the side. Add garlic and ginger to the center of the pan. 30 seconds until fragrant. Then toss everything together.",
				Duration:    30 * time.Second,
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Garlic and ginger are fragrant"},
				},
			},
			{
				ID: "vsf-7", Order: 7,
				Instruction: "Pour the sauce over everything. Toss to coat evenly. Cook for 30 more seconds until the sauce thickens slightly.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionVisual, Description: "Sauce coats vegetables, slightly glossy"},
				},
			},
			{
				ID: "vsf-8", Order: 8,
				Instruction: "Serve immediately over rice. This does not get better sitting around.",
				Conditions: []domain.StepCondition{
					{Type: domain.ConditionManual, Description: "Plated and ready"},
				},
			},
		},
		Version: 1,
	}
}
