package grok

// successHTML is served to the browser after a successful OAuth callback.
// Dark background, system-ui font, auto-close script — matches opencode's
// HTML_SUCCESS styling.
const successHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Authorization Successful</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
      background: #0d0d0d;
      color: #e5e5e5;
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
    }
    .card {
      background: #1a1a1a;
      border: 1px solid #2a2a2a;
      border-radius: 12px;
      padding: 2.5rem 3rem;
      text-align: center;
      max-width: 440px;
      width: 90%;
    }
    .icon {
      font-size: 3rem;
      margin-bottom: 1rem;
    }
    h1 {
      font-size: 1.5rem;
      font-weight: 600;
      margin-bottom: 0.75rem;
      color: #fff;
    }
    p {
      font-size: 0.95rem;
      color: #a0a0a0;
      line-height: 1.6;
    }
    .note {
      margin-top: 1.5rem;
      font-size: 0.8rem;
      color: #555;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#10003;</div>
    <h1>Authorization Successful</h1>
    <p>You have successfully authorized CLIProxyAPI. You can close this window and return to your terminal.</p>
    <p class="note">This window will close automatically.</p>
  </div>
  <script>
    setTimeout(function() { window.close(); }, 3000);
  </script>
</body>
</html>`

// errorHTML is served to the browser when the OAuth callback carries an error.
const errorHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Authorization Failed</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
      background: #0d0d0d;
      color: #e5e5e5;
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
    }
    .card {
      background: #1a1a1a;
      border: 1px solid #2a2a2a;
      border-radius: 12px;
      padding: 2.5rem 3rem;
      text-align: center;
      max-width: 440px;
      width: 90%;
    }
    .icon {
      font-size: 3rem;
      margin-bottom: 1rem;
      color: #ef4444;
    }
    h1 {
      font-size: 1.5rem;
      font-weight: 600;
      margin-bottom: 0.75rem;
      color: #fff;
    }
    p {
      font-size: 0.95rem;
      color: #a0a0a0;
      line-height: 1.6;
    }
    .error {
      margin-top: 1rem;
      font-size: 0.85rem;
      color: #ef4444;
      word-break: break-all;
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#10007;</div>
    <h1>Authorization Failed</h1>
    <p>An error occurred during authorization. Please close this window and try again.</p>
    <p class="error">{{ERROR}}</p>
  </div>
</body>
</html>`
