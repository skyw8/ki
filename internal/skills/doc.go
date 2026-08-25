// Package skills discovers SKILL.md packages for the system prompt XML.
//
// Search union: {KI_HOME}/skills, ~/.agents/skills, <cwd>/.ki/skills,
// optional extra roots (extension skills), and ancestor .agents/skills up
// to the git root. Extra roots are inserted after project .ki/skills. Each package is a top-level
// dir (symlinks followed) containing SKILL.md — the rest of the package is not
// walked. Scan is deliberately uncached; internal/resources owns the atomic
// per-session snapshot. Filter applies a runtime toggle without rescanning.
// The model loads a skill body by Read-ing its file path.
package skills
