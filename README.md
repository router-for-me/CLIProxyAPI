# CLIProxyAPI Plus

English | [Chinese](README_CN.md)

This is the Plus version of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI), adding support for third-party providers on top of the mainline project.

All third-party provider support is maintained by community contributors; CLIProxyAPI does not provide technical support. Please contact the corresponding community maintainer if you need assistance.

The Plus release stays in lockstep with the mainline features.

---

### 🟢 Custom Features (Private Build)

This specific fork contains custom functionality not available in the upstream repository:
- **Usage Persistence**: Automatically tracks token and request statistics and saves them to `usage/usage.json` every 5 minutes and during graceful shutdown. (Ported from rejected upstream PR #1944).
- **Graceful Shutdown Fix**: Resolves an upstream bug in `sdk/cliproxy/service.go` where the graceful shutdown context instantly expired, preventing proper cleanup and data saving on service stop.

See `UPDATE_INSTRUCTIONS.md` for instructions on how to pull future updates from the upstream mainline repository without losing these custom features.

---

## Contributing

This project only accepts pull requests that relate to third-party provider support. Any pull requests unrelated to third-party provider support will be rejected.

If you need to submit any non-third-party provider changes, please open them against the [mainline](https://github.com/router-for-me/CLIProxyAPI) repository.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
