// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package layered implements dependency graph construction, cycle detection,
// and topological layer calculation for Atlantis projects with depends_on
// relationships.
//
// It takes project configs and changed-file lists as input and returns layer
// assignments as output. The plan/apply lifecycle orchestrator consumes this
// module to determine which projects to plan in which order.
//
// The core algorithm builds a directed acyclic graph (DAG) from depends_on
// edges, detects cycles using Kahn's algorithm, and assigns projects to
// layers such that all dependencies of a project are in earlier layers.
// After each layer is planned, downstream projects may be dynamically
// added to subsequent layers based on whether upstream projects had actual
// infrastructure changes.
package layered
