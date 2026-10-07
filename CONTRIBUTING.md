# Contributing to Mailat

Thank you for your interest in contributing to Mailat! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go 1.27.1 (pinned by the `toolchain` line in `apps/api/go.mod`; an older Go 1.21+ downloads it automatically unless `GOTOOLCHAIN=local`)
- Node.js 24
- PostgreSQL 16 (the version CI tests against)
- Redis 7 (required: the API exits at startup without it)
- Docker, optional, for running PostgreSQL and Redis locally

### Getting Started

1. **Clone the repository**
   ```bash
   git clone https://github.com/dublyo/mailat.git
   cd mailat
   ```

2. **Install web dependencies**
   ```bash
   # The web app uses npm and apps/web/package-lock.json, the same as CI
   cd apps/web && npm ci
   ```

3. **Set up environment variables**
   ```bash
   cp .env.example .env
   # Edit .env with your configuration
   ```

4. **Database migrations**

   The versioned SQL files in `apps/api/internal/database/migrations` are the
   authoritative schema. The API applies any pending migrations on startup
   (`AUTO_MIGRATE=true`, the default), so there is no separate migration step.
   Add schema changes as a new numbered file there; never edit a migration
   that has already been released.

5. **Start services**

   Start PostgreSQL and Redis first (see the README's Local development
   section for example `docker run` commands). The root `docker-compose.yml`
   only starts the legacy Stalwart server and is not needed for SES mode.

   ```bash
   # Start API (run from apps/api so ../../.env resolves)
   cd apps/api
   go run ./cmd/server

   # Start web UI (in another terminal)
   cd apps/web
   npm run dev
   ```

6. **Access the application**
   - Web UI: http://localhost:3000
   - API: http://localhost:3001 (the Docker image listens on 8000)
   - API Docs: http://localhost:3001/docs/

## Code Style

### Go

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `golangci-lint` for linting
- Write tests for new features
- Keep functions small and focused
- Document exported functions

### TypeScript/Vue

- Follow the project's ESLint configuration
- Use Prettier for formatting
- Prefer composition API over options API
- Write TypeScript types for all props and emits
- Use composables for reusable logic

### Commits

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add email scheduling feature
fix: resolve inbox pagination bug
docs: update API documentation
chore: upgrade dependencies
refactor: simplify domain verification logic
test: add tests for campaign service
```

## Pull Request Process

1. **Fork the repository** and create a new branch from `main`
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. **Make your changes**
   - Write clean, well-documented code
   - Add tests for new features
   - Update documentation as needed

3. **Test your changes**
   ```bash
   # Go: build, vet, OpenAPI drift check and tests
   cd apps/api && go build ./... && go vet ./... && go run ./cmd/openapi --check
   go test -race ./...   # set MAILAT_TEST_DATABASE_URL to run the PostgreSQL integration tests

   # Web: tests, type check and production build
   cd apps/web && npm test && npm run build
   ```

4. **Commit your changes**
   ```bash
   git add .
   git commit -m "feat: add your feature description"
   ```

5. **Push to your fork**
   ```bash
   git push origin feature/your-feature-name
   ```

6. **Open a Pull Request**
   - Provide a clear description of the changes
   - Link any related issues
   - Add screenshots for UI changes
   - Ensure CI checks pass

## Reporting Bugs

When reporting bugs, please include:

- **Environment details**: OS, Go version, Node version
- **Steps to reproduce**: Clear, numbered steps
- **Expected behavior**: What should happen
- **Actual behavior**: What actually happens
- **Logs/Screenshots**: Any relevant error messages

Use our [bug report template](.github/ISSUE_TEMPLATE/bug_report.md).

## Feature Requests

We welcome feature requests! Please:

- Check if the feature already exists or is planned
- Describe the problem you're trying to solve
- Explain your proposed solution
- Consider implementation complexity

Use our [feature request template](.github/ISSUE_TEMPLATE/feature_request.md).

## Code Review Process

- All submissions require review before merging
- Reviewers may request changes or improvements
- Once approved, a maintainer will merge your PR
- We strive to review PRs within 48 hours

## Community Guidelines

- Be respectful and inclusive
- Follow our [Code of Conduct](CODE_OF_CONDUCT.md)
- Help others in discussions and issues
- Share knowledge and best practices

## Questions?

- **Documentation**: Check our [README](README.md)
- **Discussions**: Use [GitHub Discussions](https://github.com/dublyo/mailat/discussions)
- **Issues**: Search [existing issues](https://github.com/dublyo/mailat/issues)

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
