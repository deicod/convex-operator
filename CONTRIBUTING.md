# Contributing to convex-operator

Thank you for your interest in contributing to the convex-operator project!

## Guidelines

The project guidelines, including coding style, testing instructions, and directory structure, are maintained in **[`AGENTS.md`](./AGENTS.md)**. Please read that file carefully before starting your work.

## Quick Start

1.  **Dependencies**: Ensure you have Go 1.24+, Docker, and kubectl installed.
2.  **Linting**: Run `make fmt vet` to format and vet your code.
3.  **Testing**: Run `make test` to execute the test suite.
4.  **Local Dev**: Use `make run` to run the controller locally against your configured Kubernetes cluster.

## Pull Requests

- Follow [Conventional Commits](https://www.conventionalcommits.org/).
- Include a short summary of your changes.
- Ensure all tests pass (`make test`).
- Link to any relevant issues.

For more details, see [`AGENTS.md`](./AGENTS.md).
