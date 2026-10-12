# Contributing

Changes should keep the DRA/framework and plugin boundary explicit. Read
[development](docs/development.md), [architecture](docs/architecture.md) and the
[plugin SDK](pkg/pluginapi/README.md) first. Report problems with the topology,
policy, sanitized claim/status, driver/runtime versions and relevant errors.

Use Go 1.27 and run `make generate`, `make fmt`, `make lint`, `make test` and
`make build` before submitting a change. Keep generated CRDs synchronized with
the Helm chart. Update documentation and public examples for API/behavior changes.
Add tests for ownership, recovery and validation changes; do not add tests that
launch containerd/CRI-O binaries to the unit suite.

For live testing, use a dedicated lab and the separate manual integration
scripts. Do not mutate management interfaces or force-delete retained journals
or finalizers. State hardware/acceptance limitations in the change description.

Keep existing upstream copyright notices and Apache-2.0 licensing when reusing
code. See [NOTICE](NOTICE). Submit focused pull requests describing the concrete
problem, resulting behavior and relevant validation.
