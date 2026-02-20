# Contributing to cliproxyapi++

First off, thank you for considering contributing to **cliproxyapi++**! It's people like you who make this tool better for everyone.

## Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md) (coming soon).

## How Can I Contribute?

### Reporting Bugs
- Use the [Bug Report](https://github.com/KooshaPari/cliproxyapi-plusplus/issues/new?template=bug_report.md) template.
- Provide a clear and descriptive title.
- Describe the exact steps to reproduce the problem.

### Suggesting Enhancements
- Check the [Issues](https://github.com/KooshaPari/cliproxyapi-plusplus/issues) to see if the enhancement has already been suggested.
- Use the [Feature Request](https://github.com/KooshaPari/cliproxyapi-plusplus/issues/new?template=feature_request.md) template.

### Pull Requests
1. Fork the repo and create your branch from `main`.
2. If you've added code that should be tested, add tests.
3. If you've changed APIs, update the documentation.
4. Ensure the test suite passes (`go test ./...`).
5. Make sure your code lints (`golangci-lint run`).

#### Which repository to use?
- **Third-party provider support**: Submit your PR directly to [KooshaPari/cliproxyapi-plusplus](https://github.com/KooshaPari/cliproxyapi-plusplus).
- **Core logic improvements**: If the change is not specific to a third-party provider, please propose it to the [mainline project](https://github.com/router-for-me/CLIProxyAPI) first.

## Governance

This project follows a community-driven governance model. Major architectural decisions are discussed in Issues before implementation.

### Path Guard
We use a `pr-path-guard` to protect critical translator logic. Changes to these paths require explicit review from project maintainers to ensure security and stability.

---
Thank you for your contributions!
