package provider

import (
	"math/rand"
	"sort"
	"sync/atomic"
)

// LoadBalancer reorders routes for load distribution.
type LoadBalancer interface {
	Reorder(routes []Route) []Route
}

// NewLoadBalancer creates a load balancer by strategy name.
func NewLoadBalancer(strategy string) LoadBalancer {
	switch strategy {
	case "round-robin":
		return &RoundRobinBalancer{}
	case "random":
		return &RandomBalancer{}
	case "least-cost":
		return &LeastCostBalancer{}
	default:
		return &FirstBalancer{}
	}
}

// FirstBalancer is a no-op that preserves original order.
type FirstBalancer struct{}

func (b *FirstBalancer) Reorder(routes []Route) []Route {
	return routes
}

// RoundRobinBalancer rotates routes within same-priority groups.
type RoundRobinBalancer struct {
	counter atomic.Uint64
}

func (b *RoundRobinBalancer) Reorder(routes []Route) []Route {
	if len(routes) <= 1 {
		return routes
	}

	result := make([]Route, len(routes))
	copy(result, routes)

	// Group by priority and rotate within each group
	groups := groupByPriority(result)
	idx := 0
	count := b.counter.Add(1)
	for _, group := range groups {
		if len(group) > 1 {
			offset := int(count) % len(group)
			for j := 0; j < len(group); j++ {
				result[idx] = group[(j+offset)%len(group)]
				idx++
			}
		} else {
			result[idx] = group[0]
			idx++
		}
	}

	return result
}

// RandomBalancer shuffles routes within same-priority groups.
type RandomBalancer struct{}

func (b *RandomBalancer) Reorder(routes []Route) []Route {
	if len(routes) <= 1 {
		return routes
	}

	result := make([]Route, len(routes))
	copy(result, routes)

	groups := groupByPriority(result)
	idx := 0
	for _, group := range groups {
		rand.Shuffle(len(group), func(i, j int) {
			group[i], group[j] = group[j], group[i]
		})
		for _, r := range group {
			result[idx] = r
			idx++
		}
	}

	return result
}

// LeastCostBalancer sorts by price within same-priority groups.
type LeastCostBalancer struct{}

func (b *LeastCostBalancer) Reorder(routes []Route) []Route {
	if len(routes) <= 1 {
		return routes
	}

	result := make([]Route, len(routes))
	copy(result, routes)

	groups := groupByPriority(result)
	idx := 0
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			costI := group[i].InputPrice + group[i].OutputPrice
			costJ := group[j].InputPrice + group[j].OutputPrice
			return costI < costJ
		})
		for _, r := range group {
			result[idx] = r
			idx++
		}
	}

	return result
}

// groupByPriority splits routes into groups of same priority, preserving order.
func groupByPriority(routes []Route) [][]Route {
	if len(routes) == 0 {
		return nil
	}

	var groups [][]Route
	currentPriority := routes[0].Priority
	currentGroup := []Route{routes[0]}

	for i := 1; i < len(routes); i++ {
		if routes[i].Priority == currentPriority {
			currentGroup = append(currentGroup, routes[i])
		} else {
			groups = append(groups, currentGroup)
			currentPriority = routes[i].Priority
			currentGroup = []Route{routes[i]}
		}
	}
	groups = append(groups, currentGroup)

	return groups
}
