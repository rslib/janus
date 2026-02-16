package provider

import (
	"fmt"
	"testing"
)

type routeSpec struct {
	name     string
	priority int
	input    float64
	output   float64
}

func makeRoutes(specs ...routeSpec) []Route {
	routes := make([]Route, len(specs))
	for i, s := range specs {
		routes[i] = Route{
			Provider:      &mockProvider{name: s.name},
			ProviderModel: s.name + "-model",
			Priority:      s.priority,
			InputPrice:    s.input,
			OutputPrice:   s.output,
		}
	}
	return routes
}

func routeNames(routes []Route) []string {
	names := make([]string, len(routes))
	for i, r := range routes {
		names[i] = r.Provider.Name()
	}
	return names
}

func TestFirstBalancer_PreservesOrder(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"a", 1, 1.0, 1.0},
		routeSpec{"b", 1, 2.0, 2.0},
		routeSpec{"c", 1, 3.0, 3.0},
	)

	b := &FirstBalancer{}
	result := b.Reorder(routes)

	names := routeNames(result)
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("expected [a b c], got %v", names)
	}
}

func TestRoundRobinBalancer_RotatesWithinPriorityGroup(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"a", 1, 1.0, 1.0},
		routeSpec{"b", 1, 1.0, 1.0},
		routeSpec{"c", 1, 1.0, 1.0},
	)

	b := &RoundRobinBalancer{}

	// Collect the first element from multiple calls
	seen := make(map[string]bool)
	for i := 0; i < 6; i++ {
		result := b.Reorder(routes)
		seen[result[0].Provider.Name()] = true
	}

	// All routes should have appeared as first at some point
	for _, name := range []string{"a", "b", "c"} {
		if !seen[name] {
			t.Errorf("expected %q to appear as first element in rotation", name)
		}
	}
}

func TestRoundRobinBalancer_PreservesPriorityOrder(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"a", 1, 1.0, 1.0},
		routeSpec{"b", 1, 1.0, 1.0},
		routeSpec{"c", 2, 1.0, 1.0},
	)

	b := &RoundRobinBalancer{}

	// Priority 2 route should always be last
	for i := 0; i < 5; i++ {
		result := b.Reorder(routes)
		if result[2].Provider.Name() != "c" {
			t.Fatalf("expected priority-2 route 'c' at the end, got %q", result[2].Provider.Name())
		}
	}
}

func TestRandomBalancer_AllRoutesPresent(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"a", 1, 1.0, 1.0},
		routeSpec{"b", 1, 1.0, 1.0},
		routeSpec{"c", 1, 1.0, 1.0},
	)

	b := &RandomBalancer{}

	for i := 0; i < 10; i++ {
		result := b.Reorder(routes)
		if len(result) != 3 {
			t.Fatalf("expected 3 routes, got %d", len(result))
		}

		names := make(map[string]bool)
		for _, r := range result {
			names[r.Provider.Name()] = true
		}
		for _, want := range []string{"a", "b", "c"} {
			if !names[want] {
				t.Errorf("missing route %q in result", want)
			}
		}
	}
}

func TestRandomBalancer_PreservesPriorityOrder(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"a", 1, 1.0, 1.0},
		routeSpec{"b", 1, 1.0, 1.0},
		routeSpec{"c", 2, 1.0, 1.0},
	)

	b := &RandomBalancer{}

	for i := 0; i < 10; i++ {
		result := b.Reorder(routes)
		if result[2].Provider.Name() != "c" {
			t.Fatalf("expected priority-2 route 'c' last, got %q", result[2].Provider.Name())
		}
	}
}

func TestLeastCostBalancer_SortsByCost(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"expensive", 1, 10.0, 10.0},
		routeSpec{"cheap", 1, 1.0, 1.0},
		routeSpec{"medium", 1, 5.0, 5.0},
	)

	b := &LeastCostBalancer{}
	result := b.Reorder(routes)

	names := routeNames(result)
	expected := []string{"cheap", "medium", "expensive"}
	for i, want := range expected {
		if names[i] != want {
			t.Errorf("position %d: got %q, want %q", i, names[i], want)
		}
	}
}

func TestLeastCostBalancer_PreservesPriorityOrder(t *testing.T) {
	routes := makeRoutes(
		routeSpec{"expensive-p1", 1, 10.0, 10.0},
		routeSpec{"cheap-p1", 1, 1.0, 1.0},
		routeSpec{"cheap-p2", 2, 0.5, 0.5},
	)

	b := &LeastCostBalancer{}
	result := b.Reorder(routes)

	names := routeNames(result)
	// Within priority 1, cheap should come first; priority 2 always last
	if names[0] != "cheap-p1" {
		t.Errorf("expected cheap-p1 first, got %q", names[0])
	}
	if names[1] != "expensive-p1" {
		t.Errorf("expected expensive-p1 second, got %q", names[1])
	}
	if names[2] != "cheap-p2" {
		t.Errorf("expected cheap-p2 last, got %q", names[2])
	}
}

func TestGroupByPriority(t *testing.T) {
	tests := []struct {
		name       string
		priorities []int
		wantGroups [][]int
	}{
		{
			name:       "empty",
			priorities: nil,
			wantGroups: nil,
		},
		{
			name:       "single",
			priorities: []int{1},
			wantGroups: [][]int{{1}},
		},
		{
			name:       "all same",
			priorities: []int{1, 1, 1},
			wantGroups: [][]int{{1, 1, 1}},
		},
		{
			name:       "two groups",
			priorities: []int{1, 1, 2, 2},
			wantGroups: [][]int{{1, 1}, {2, 2}},
		},
		{
			name:       "three groups",
			priorities: []int{1, 2, 2, 3},
			wantGroups: [][]int{{1}, {2, 2}, {3}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var routes []Route
			for _, p := range tt.priorities {
				routes = append(routes, Route{Priority: p})
			}

			groups := groupByPriority(routes)

			if tt.wantGroups == nil {
				if groups != nil {
					t.Fatalf("expected nil groups, got %v", groups)
				}
				return
			}

			if len(groups) != len(tt.wantGroups) {
				t.Fatalf("expected %d groups, got %d", len(tt.wantGroups), len(groups))
			}

			for i, wg := range tt.wantGroups {
				if len(groups[i]) != len(wg) {
					t.Errorf("group %d: expected %d routes, got %d", i, len(wg), len(groups[i]))
					continue
				}
				for j, wp := range wg {
					if groups[i][j].Priority != wp {
						t.Errorf("group %d, route %d: expected priority %d, got %d", i, j, wp, groups[i][j].Priority)
					}
				}
			}
		})
	}
}

func TestBalancer_SingleRoute(t *testing.T) {
	routes := makeRoutes(routeSpec{"only", 1, 1.0, 1.0})

	balancers := []struct {
		name     string
		balancer LoadBalancer
	}{
		{"first", &FirstBalancer{}},
		{"round-robin", &RoundRobinBalancer{}},
		{"random", &RandomBalancer{}},
		{"least-cost", &LeastCostBalancer{}},
	}

	for _, bb := range balancers {
		t.Run(bb.name, func(t *testing.T) {
			result := bb.balancer.Reorder(routes)
			if len(result) != 1 || result[0].Provider.Name() != "only" {
				t.Fatalf("expected single route 'only', got %v", routeNames(result))
			}
		})
	}
}

func TestNewLoadBalancer(t *testing.T) {
	tests := []struct {
		strategy string
		wantType string
	}{
		{"round-robin", "*provider.RoundRobinBalancer"},
		{"random", "*provider.RandomBalancer"},
		{"least-cost", "*provider.LeastCostBalancer"},
		{"first", "*provider.FirstBalancer"},
		{"unknown", "*provider.FirstBalancer"},
		{"", "*provider.FirstBalancer"},
	}

	for _, tt := range tests {
		t.Run(tt.strategy, func(t *testing.T) {
			b := NewLoadBalancer(tt.strategy)
			got := fmt.Sprintf("%T", b)
			if got != tt.wantType {
				t.Errorf("NewLoadBalancer(%q) = %s, want %s", tt.strategy, got, tt.wantType)
			}
		})
	}
}
