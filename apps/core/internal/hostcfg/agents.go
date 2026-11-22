package hostcfg

// The core ships operating guidance for coding agents a user installs on
// the host (image/core/usr/share/homelab/agent). The agents look for it
// under /etc, which a system extension cannot reach, so the host's
// configuration extension points each agent's system location at it. The
// text changes with the core; these pointers do not.
const (
	// agentDir is where the core extension puts the guidance.
	agentDir   = "/usr/share/homelab/agent"
	agentBrief = agentDir + "/brief.md"
	agentSkill = agentDir + "/skills/homelab"
)

// codexConfig is Codex's system config layer. Codex has no system-wide
// instructions file, only this inline string, so it points at the brief.
const codexConfig = `# Written by homelab-daemon; changes are lost.
developer_instructions = """
This machine runs homelab-ngx. Before you work with its stacks, services or logs, read ` + agentBrief + ` and use the homelab skill.
"""
`

// opencodeConfig is opencode's system config. Its arrays add to the
// user's and the project's own.
const opencodeConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "instructions": ["` + agentBrief + `"],
  "skills": ["` + agentDir + `/skills"]
}
`

// agentEtc adds the agents' system files and links to the confext's.
func agentEtc(files, links map[string]string) {
	// Claude Code: the managed CLAUDE.md, and a skill in the managed
	// skills folder.
	links["etc/claude-code/CLAUDE.md"] = agentBrief
	links["etc/claude-code/.claude/skills/homelab"] = agentSkill
	// Codex: the admin skills folder, which follows links.
	files["etc/codex/config.toml"] = codexConfig
	links["etc/codex/skills/homelab"] = agentSkill
	// opencode: the system config, which names both.
	files["etc/opencode/opencode.json"] = opencodeConfig
}
