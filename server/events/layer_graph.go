// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"
	"strings"
)

// detectCycles checks for circular dependencies in a map[string][]string graph.
// This is intentionally separate from layered.DependencyGraph.detectCycle() —
// that operates on the typed DependencyGraph struct, while this operates on the
// simpler map representation used by LayerState.DependencyGraph.
// Returns an error with the cycle path if found.
func detectCycles(graph map[string][]string) error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int)
	path := make(map[string]bool)
	var pathSlice []string

	var dfs func(node string) error
	dfs = func(node string) error {
		state[node] = visiting
		path[node] = true
		pathSlice = append(pathSlice, node)

		for _, dep := range graph[node] {
			if state[dep] == visiting && path[dep] {
				// Found a cycle - build the cycle path
				cycleStart := -1
				for i, n := range pathSlice {
					if n == dep {
						cycleStart = i
						break
					}
				}
				cyclePath := append(pathSlice[cycleStart:], dep)
				return fmt.Errorf("circular dependency detected: %s", strings.Join(cyclePath, " -> "))
			}
			if state[dep] == unvisited {
				if err := dfs(dep); err != nil {
					return err
				}
			}
		}

		pathSlice = pathSlice[:len(pathSlice)-1]
		delete(path, node)
		state[node] = visited
		return nil
	}

	for node := range graph {
		if state[node] == unvisited {
			if err := dfs(node); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildReverseDependencyMap inverts the dependency graph.
// Input: project -> [dependencies]
// Output: project -> [dependents]
func buildReverseDependencyMap(graph map[string][]string) map[string][]string {
	reverse := make(map[string][]string)
	for project, deps := range graph {
		for _, dep := range deps {
			reverse[dep] = append(reverse[dep], project)
		}
	}
	return reverse
}

// deduplicate returns a slice with unique elements, preserving order.
func deduplicate(items []string) []string {
	if items == nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
