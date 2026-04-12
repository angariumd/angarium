# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0-alpha] - 2026-04-09

### Added
- Initial alpha release of Angarium GPU scheduler.
- Lightweight controller with SQLite backend.
- Bare-metal agent for Linux/NVIDIA environments.
- Support for GPU leases and fault detection (stale nodes/zombie jobs).
- Terminal-friendly CLI for job submission and status monitoring.
- Log streaming support for active and completed jobs.
- Hash-based user authentication (SHA-256).
- Configurable TLS verification and log directories.

### Fixed
- Fixed critical bug where all state updates emitted `JOB_CANCELED` events.
- Fixed non-existent Go version in `go.mod`.
- Fixed year bug in SQL datetime formatting.
- Removed hardcoded developer credentials from CLI.
- Fixed fragmented capacity reporting in scheduler feedback.
- Fixed test assertion errors in scheduler.
