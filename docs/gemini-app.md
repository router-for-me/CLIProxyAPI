# Gemini App

The `gemini-app` provider uses a signed-in [Gemini App](https://gemini.google.com/app)
browser session. It does not require a Gemini API key or Gemini CLI credentials.

## Import a session

1. Sign in to Gemini App in your browser and confirm that text chat works.
2. Export the `__Secure-1PSID` and, when present, `__Secure-1PSIDTS` cookies as a
   JSON file. Both a cookie-name object and a browser cookie export array are accepted.
   For example, create `gemini-cookies.json` with this structure and your own values:

   ```json
   {
     "__Secure-1PSID": "your-cookie-value",
     "__Secure-1PSIDTS": "your-cookie-value"
   }
   ```

3. Import the file using the same configuration as the server:

   ```sh
   cli-proxy-api --config config.yaml --gemini-app-import gemini-cookies.json
   ```

The importer validates the session and saves a `gemini-app-*.json` credential
under the configured `auth-dir`. The server discovers this file through its
existing auth-file watcher. Import each account separately for normal account
selection and load balancing. Browser cookies are credentials: keep the export
and saved auth file private, and delete the export after importing it.

## Send a request

Use `gemini-app` from `/v1/models` as the model name. For example:

```sh
curl http://localhost:8317/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemini-app","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

OpenAI, Claude, and Gemini text requests use the existing protocol translators.
Streaming delivers text deltas; non-streaming waits for a complete answer.
The model is the account's App default, whose availability and quota depend on
Google and the subscription. No API model version or tier is implied.

Each request creates a temporary chat and includes the full supplied text
history, with system, user, and assistant role labels rendered into one prompt.
System instructions therefore do not have a separate privileged API role.
Sampling parameters and output token limits are controlled by Gemini App, not
the API fields. Tool calling, media input/output, token counting, and compact
responses are unsupported. Token usage is not reported by the App endpoint.

Cookies do not have OAuth refresh tokens. If authentication expires, sign in
again and re-import fresh cookies. Upstream Google web protocol changes can
require an update to this provider.

## Payload rules

Payload rules with `protocol: gemini-app` target the final App request array,
after history rendering and all built-in defaults. For example, the prompt is
at path `0.0`, and the temporary-chat flag is at path `45`. The finalized array
is only JSON-encoded and form-wrapped before sending; configured values are
never replaced by subsequent business logic.
