# Standard Dynamic Library Plugin Examples

This directory contains standard dynamic library plugin examples for the CLIProxyAPI C ABI.

## Layout

- `simple/`: full provider-native skeleton that declares every supported capability.
- `model/`: model capability only.
- `auth/`: auth provider capability only.
- `frontend-auth/`: frontend auth provider capability only.
- `executor/`: executor capability only.
- `protocol-format/`: minimal executor focused on input/output format declarations.
- `request-translator/`: request translation capability only.
- `request-normalizer/`: request normalization capability only.
- `response-translator/`: response translation capability only.
- `response-normalizer/`: response normalization capability only.
- `thinking/`: thinking applier capability only.
- `usage/`: usage observer capability only.
- `cli/`: command-line capability only.
- `management-api/`: Management API capability only.
- `host-callback/`: minimal Management API route that demonstrates host callbacks.

Each example directory contains `go/`, `c/`, and `rust/` subdirectories.

## Build All Examples

```bash
make -C examples/plugin list
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin`.

## Notes

`protocol-format` uses a minimal executor because format declarations belong to executor capabilities.

`host-callback` uses a minimal Management API route because host callbacks are invoked from plugin methods and are not standalone capabilities.
