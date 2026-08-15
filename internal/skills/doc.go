// Package skills discovers SKILL.md packages for the system prompt XML.
//
// Search union: {KI_HOME}/skills, ~/.agents/skills, <cwd>/.ki/skills,
// and ancestor .agents/skills up to the git root. Session Toggle applies
// only/disabled. The model loads a skill by Read-ing its file path.
package skills
