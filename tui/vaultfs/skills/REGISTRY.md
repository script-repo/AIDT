# Skill Registry

**Every agent on this host checks this file before installing anything.**

Seven coding agents share this machine. If each installs its own variant of the
same tool, the host ends up with seven half-working configurations and no agent
can depend on any of them. So the rule is: look here first, use what exists,
and document what you add.

## How to use it

```bash
aidt-skill list              # the table below, from the shell
aidt-skill show <name>       # read a skill's SKILL.md
aidt-skill new <name>        # scaffold a new skill from TEMPLATE.md
aidt-skill register          # rescan skills/ and rebuild the table below
```

## Before you install anything

1. `aidt-skill list` — is it already here?
2. If yes: read the SKILL.md and **use it as documented**. Do not reimplement.
3. If no, and you genuinely need it:
   - `aidt-skill new <name>`
   - Fill in `skills/<name>/SKILL.md` — especially **Prerequisites**,
     **Usage**, and **Failure modes**. A skill whose docs lie is worse than no
     skill at all.
   - `aidt-skill register`
   - *Then* install it.
4. If a skill is broken, mark `status: broken` in its frontmatter, re-register,
   and either fix it or file a task. Do not silently work around it — the next
   agent will hit the same wall.

Anything reusable counts as a skill: a configured CLI, an MCP server, a helper
script, an API recipe, a prompt pattern that reliably works. If you had to
figure it out once, write it down so nobody figures it out twice.

---

## Installed skills

<!-- BEGIN AIDT-SKILL-TABLE -->
| Skill | Status | Owner | Summary |
| --- | --- | --- | --- |
<!-- END AIDT-SKILL-TABLE -->

_Regenerate this table with `aidt-skill register`. Do not hand-edit it._
