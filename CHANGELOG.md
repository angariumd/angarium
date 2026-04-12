# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1-alpha] - 2026-04-12

### Added
- Interactive `login` command to CLI for streamlined authentication and controller selection.
- `whoami` command to CLI for verifying current identity and controller connectivity.
- Support for system-wide memory metrics in node heartbeats and `status` output.
- Automatic agent state persistence and recovery to handle agent restarts without losing track of running jobs.

### Changed
- Improved `logs` command with better streaming stability and error reporting for non-existent jobs.
- Enhanced scheduler feedback with detailed reasons for queued jobs (e.g., fragmentation vs cluster capacity).

### Fixed
- Fixed scheduler bug to fail jobs immediately if requested GPU count exceeds any single node's total capacity.
- Improved concurrency in the scheduler by making agent launch notifications asynchronous.
- Added missing indexes to the `events` table for better performance on large history.

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
