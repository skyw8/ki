// Package memory estimates retained Go object graphs for cache admission.
// Budgets bound cache ownership, not process RSS: active requests, allocator
// slack and runtime metadata have independent lifetimes. Shared strings,
// pointers, slices and maps are charged once within each measured graph.
package memory
