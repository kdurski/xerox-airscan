# Contributing

Build with Go 1.25 or newer. Run `make test check` and format changes with
`gofmt -w cmd internal` before submitting a pull request. The default tests are
self-contained and do not require a scanner. See the README for optional private
capture regressions and native macOS checks.

Keep protocol changes covered by synthetic fixtures or public test images.
Do not commit real scanned documents, packet captures, printer configuration,
device identifiers, credentials, or debug logs. Local captures can be used through
`XEROX_TEST_CAPTURE_DIR`; share only the smallest redacted protocol excerpt needed
to explain a bug.

For device issues, include the model, macOS version, selected source, resolution,
and whether the problem occurs with a flatbed page, feeder page, or batch.
The WorkCentre 3225 is currently the only validated model.

Contributions are licensed under the project's MIT License.
