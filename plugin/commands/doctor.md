---
description: Diagnose the local Claude2Kiro setup without sending a model request
allowed-tools:
  - Bash
---

Run the read-only Claude2Kiro health report:

```bash
claude2kiro doctor --json
```

The report never contains token values. It has one entry per check, each with a
severity of `ok`, `warn` or `error`. Present a short summary:
- every `error` first, with its detail and the command the detail suggests,
- then every `warn`,
- then one line saying how many checks passed.

If the `claude2kiro` command is not found, say that the launcher is not on PATH
and point to the install instructions in the project README.
