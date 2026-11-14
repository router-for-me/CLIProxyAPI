# Externally owned custom executors

Embedders can use `Builder.WithOwnedProviderExecutor(provider, executor)` to keep
an externally managed custom executor for one Service lifetime. The Service uses
the original object on startup and when credentials or configuration cause
executor registration. A forced reload does not replace an opted-in owner with
an OpenAI-compatible executor.

This is optional. Registering an executor only through `Manager.RegisterExecutor`
retains the existing behavior, including replacement during a forced service
reload. Existing native and dynamic plugin registration rules are unchanged for
providers without an owner.

## Keys and validation

Build validates every registration before installing any owner in the supplied
manager. A missing executor, typed nil, unsupported equality comparison,
duplicate provider, or mismatched Identifier is an error. Executors must compare
equal to themselves so Manager can recognize the same executor. Value executors
containing NaN are therefore rejected.
Provider arguments are trimmed and lowered;
the executor Identifier must already be that stable canonical key. The built
Service holds a private copy of the registrations. Later Builder changes do not
change that Service.

Native provider keys and the `openai-compatible-` namespace are reserved. Explicit
OpenAI-compatible auth follows the existing compatibility-key resolution before
owned custom lookup. For example, a custom `example-provider` and an explicit
OpenAI-compatible entry named `example-provider` use different executor keys.
An auth with `compat_name` also follows that explicit compatibility path, even if
its Provider is the custom key.

An auth's `base_url` alone does not change its custom provider key. For an owned
key, such metadata is passed to the external executor; it does not select a stock
fallback. The owner must validate and interpret the credential and custom
settings it supports. A disabled credential remains ineligible under existing
manager rules.

## Configuration and lifetime

This option is not an executor factory. The external owner handles its custom
endpoint, transport and other configuration changes, or restarts the Service to
install a new owner. Stock configuration, credential watching, model registration,
selection, retry and refresh continue through their existing paths. The executor
is not wrapped, so its optional interfaces and result behavior are preserved.

There is no ownership mutation or removal API during a Service lifetime. Do not
replace or unregister the owned manager key directly. Registration restores the
same owner if the binding is absent; it does not choose a generic replacement.
Unconfigured provider keys still use normal stock behavior.

The Service does not retire an owner merely because its configuration reloads.
Existing calls retain the executor they captured. The embedder must keep the
object usable until admitted work has ended and Service shutdown has completed,
then close externally owned resources. Normal explicit execution-session close
operations still apply. This option does not add a request-drain or resource-join
guarantee to Service shutdown.
