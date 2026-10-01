// Package tool defines provider-neutral tool contracts, canonical names and
// aliases, schema validation, and registries. Tools publish one canonical schema;
// registries accept canonical, PascalCase and registered native aliases while
// rejecting collisions. ExecutionIdentity is private host attribution passed
// through tool contexts, never model-visible parameters.
//
// Implementations live in internal/tool/builtin or internal/extension. This
// package does not import the main loop, builtins, or runtime services. Output
// storage is a separate package applied by the loop after AfterTool.
// Cross-package contracts: docs/tools.md.
package tool
