// Package support contains argument validation and result construction shared
// by built-in adapters. It depends on tool contracts, not loop orchestration or
// runtime services, and is private to the built-in tool families.
// Integer conversion is checked before narrowing; adapters own their argument
// ranges and clamp policies independently of the shared schema subset.
package support
