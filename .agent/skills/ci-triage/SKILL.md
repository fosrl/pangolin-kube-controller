---
name: ci-triage
description: Diagnose and fix CI failures locally using the repository's Taskfile and tooling. Use when GitHub Actions fail or checks are red.
---

# CI Triage Skill

## When to use

- GitHub Actions failures
- Lint/test failures
- Coverage issues

---

## Reproduce locally

```bash
task ci
```

---

## Individual steps

```bash
task fmt
task lint
task test
go vet ./...
```

---

## Additional tools

```bash
golangci-lint run --timeout=5m
markdownlint-cli2 "**/*.md"
yamllint -c .yamllint.yaml .
hadolint Dockerfile
```

---

## Debugging checklist

- Does failure reproduce locally?
- Tool versions match CI?
- Path or OS differences?
- Missing dependencies?

---

## Rules

- Do NOT disable checks
- Do NOT weaken lint rules
- Fix root cause only

---

## Output

- Root cause
- Minimal fix
- Commands run
- Files affected
