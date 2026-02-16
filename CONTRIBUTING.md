# Contributing to Mailat

Thank you for your interest in contributing to Mailat! This document provides guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- Go 1.24 or higher
- Node.js 20 or higher
- Docker and Docker Compose
- PostgreSQL 17+ (or use Supabase/Neon)
- Redis 7+ (or use Upstash)

### Getting Started

1. **Clone the repository**
   ```bash
   git clone https://github.com/dublyo/mailat.git
   cd mailat
   ```

2. **Install dependencies**
   ```bash
   # Install pnpm if you haven't already
   npm install -g pnpm

   # Install dependencies
   pnpm install
   ```

3. **Set up environment variables**
   ```bash
   cp .env.example .env
   # Edit .env with your configuration
   ```

4. **Run database migrations**
   ```bash
   cd prisma
   npx prisma migrate dev
   ```

5. **Start services**
   ```bash
   # Start Stalwart mail server
   docker-compose up -d

   # Start API (in another terminal)
   cd apps/api
   go run cmd/server/main.go

   # Start web UI (in another terminal)
   cd apps/web
   npm run dev
   ```

6. **Access the application**
   - Web UI: http://localhost:3000
   - API: http://localhost:8000
   - API Docs: http://localhost:8000/docs

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
   # Run Go tests
   cd apps/api && go test ./...

   # Run TypeScript type checking
   cd apps/web && npm run type-check

   # Build to ensure no errors
   npm run build
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
