// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"sort"
	"testing"

	. "github.com/runatlantis/atlantis/testing"
)

func TestDetectCycles_NoCycle(t *testing.T) {
	graph := map[string][]string{
		"A": {},
		"B": {"A"},
		"C": {"B"},
	}
	err := detectCycles(graph)
	Ok(t, err)
}

func TestDetectCycles_EmptyGraph(t *testing.T) {
	graph := map[string][]string{}
	err := detectCycles(graph)
	Ok(t, err)
}

func TestDetectCycles_SelfReferencing(t *testing.T) {
	graph := map[string][]string{
		"A": {"A"},
	}
	err := detectCycles(graph)
	Assert(t, err != nil, "expected error for self-referencing cycle")
	ErrContains(t, "circular dependency", err)
}

func TestDetectCycles_TwoNodeCycle(t *testing.T) {
	graph := map[string][]string{
		"A": {"B"},
		"B": {"A"},
	}
	err := detectCycles(graph)
	Assert(t, err != nil, "expected error for two-node cycle")
	ErrContains(t, "circular dependency", err)
}

func TestDetectCycles_DeepCycle(t *testing.T) {
	graph := map[string][]string{
		"A": {"B"},
		"B": {"C"},
		"C": {"A"},
	}
	err := detectCycles(graph)
	Assert(t, err != nil, "expected error for deep cycle")
	ErrContains(t, "circular dependency", err)
}

func TestDetectCycles_DiamondNoCycle(t *testing.T) {
	graph := map[string][]string{
		"A": {},
		"B": {"A"},
		"C": {"A"},
		"D": {"B", "C"},
	}
	err := detectCycles(graph)
	Ok(t, err)
}

func TestBuildReverseDependencyMap_Simple(t *testing.T) {
	graph := map[string][]string{
		"A": {},
		"B": {"A"},
		"C": {"A", "B"},
	}
	reverse := buildReverseDependencyMap(graph)

	// A is depended on by B and C
	sort.Strings(reverse["A"])
	Equals(t, []string{"B", "C"}, reverse["A"])

	// B is depended on by C
	Equals(t, []string{"C"}, reverse["B"])

	// C has no dependents
	Equals(t, 0, len(reverse["C"]))
}

func TestBuildReverseDependencyMap_Empty(t *testing.T) {
	graph := map[string][]string{}
	reverse := buildReverseDependencyMap(graph)
	Equals(t, 0, len(reverse))
}

func TestBuildReverseDependencyMap_NoDeps(t *testing.T) {
	graph := map[string][]string{
		"A": {},
		"B": {},
	}
	reverse := buildReverseDependencyMap(graph)
	Equals(t, 0, len(reverse))
}

func TestDeduplicate_Empty(t *testing.T) {
	result := deduplicate(nil)
	Equals(t, 0, len(result))
}

func TestDeduplicate_NoDuplicates(t *testing.T) {
	result := deduplicate([]string{"A", "B", "C"})
	Equals(t, []string{"A", "B", "C"}, result)
}

func TestDeduplicate_WithDuplicates(t *testing.T) {
	result := deduplicate([]string{"A", "B", "A", "C", "B"})
	Equals(t, []string{"A", "B", "C"}, result)
}

func TestDeduplicate_AllSame(t *testing.T) {
	result := deduplicate([]string{"A", "A", "A"})
	Equals(t, []string{"A"}, result)
}
