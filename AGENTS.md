# Project instructions

Build `dbaui`, a simple Go web application that aggregates GitHub Dependabot alerts.

## Requirements

- Use Go and the chi router. Keep dependencies to a minimum and prefer the standard library where practical.
- Provide a polished, responsive interface using shadcn/ui. Keep the frontend setup as small as practical.
- Allow users to configure multiple GitHub personal access tokens (PATs) and their associated GitHub accounts and organizations.
- Display all Dependabot alerts across the configured accounts and organizations in one UI. Handle GitHub API pagination so alerts are not silently omitted.
- Treat PATs as secrets. Do not expose them in logs, error messages, or alert responses.

## Workflow and writing

- Work in meaningful, manageable atomic tasks. Avoid splitting work into trivial steps.
- Stop after completing each task and wait for the user to continue before starting the next task.
- End each task with a concise description of the changes, relevant validation results, and a suggested conventional commit message.
- Use normal hyphens instead of em dashes in code, documentation, UI text, and responses.
- Keep documentation concise. Avoid unnecessary blank lines while preserving readable Markdown formatting.
