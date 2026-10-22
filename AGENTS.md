# Project instructions

Build `dbaui` (DependaBot Alerts UI), a simple Go web application that aggregates GitHub Dependabot alerts.

## Requirements

- Use Go and the chi router. Keep dependencies to a minimum and prefer the standard library where practical.
- Render the UI with Go HTML templates. Preserve shadcn/ui styling through locally owned HTML and CSS components. Do not add React, a Node.js frontend, or a frontend build pipeline.
- Use readable text sizes and a functional interface. Avoid promotional copy, workspace panels, privacy badges, and decorative reassurance components.
- Allow users to configure multiple GitHub personal access tokens (PATs) and their associated GitHub accounts and organizations.
- Display all Dependabot alerts across the configured accounts and organizations in one UI. Handle GitHub API pagination so alerts are not silently omitted.
- Treat PATs as secrets. Do not expose them in logs, error messages, or alert responses.
- Persist PATs and connection configuration in SQLite, shared across the deployment. Encrypt PATs at rest and retain the encryption key alongside deployment data unless an external key file is configured.
- Provide a Dockerfile and sample Docker Compose deployment with persistent storage. Keep deployment setup simple.

## Workflow and writing

- Work in substantial, cohesive tasks that deliver a usable feature or milestone. Group related implementation, UI, tests, and documentation into one task to avoid excessive commits.
- Stop after completing each task and wait for the user to continue before starting the next task.
- End each task with a concise description of the changes, relevant validation results, and a suggested conventional commit message.
- Use normal hyphens instead of em dashes in code, documentation, UI text, and responses.
- Keep documentation concise. Avoid unnecessary blank lines while preserving readable Markdown formatting.
