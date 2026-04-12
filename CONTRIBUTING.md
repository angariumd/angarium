# Contributing to Angarium

First off, thank you for considering contributing to Angarium! It's people like you that make Angarium a great tool for the community.

## Development Setup

Angarium is written in Go. You'll need Go 1.24+ installed.

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/angariumd/angarium.git
    cd angarium
    ```

2.  **Build the binaries:**
    ```bash
    make build
    ```

3.  **Run unit tests:**
    ```bash
    make test
    ```

4.  **Run the smoke test:**
    The smoke test runs a full controller and agent locally to verify end-to-end functionality.
    ```bash
    ./smoke_test.sh
    ```

## Code Guidelines

- We follow idiomatic Go patterns. Run `go fmt ./...` before submitting.
- Write tests for new features.
- Keep dependencies to a minimum. We prefer the standard library or small, single-purpose libraries.

## Security

If you find a security vulnerability, please do NOT open an issue. Instead send an email to [samfrmd[at]gmail.com]

## License

By contributing, you agree that your contributions will be licensed under the project's license (MIT).
