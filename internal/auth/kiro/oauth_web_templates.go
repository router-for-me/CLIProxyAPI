// Package kiro provides OAuth Web authentication templates.
package kiro

const (
	oauthWebStartPageHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>AWS SSO Authentication</title>
    <style>
        * { box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            margin: 0;
            padding: 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
        }
        .container {
            max-width: 500px;
            width: 100%;
            background: #fff;
            padding: 40px;
            border-radius: 12px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.2);
        }
        h1 {
            margin: 0 0 10px;
            color: #333;
            font-size: 24px;
            text-align: center;
        }
        .subtitle {
            text-align: center;
            color: #666;
            margin-bottom: 30px;
        }
        .step {
            background: #f8f9fa;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 15px;
        }
        .step-title {
            display: flex;
            align-items: center;
            font-weight: 600;
            color: #333;
            margin-bottom: 10px;
        }
        .step-number {
            width: 28px;
            height: 28px;
            background: #667eea;
            color: white;
            border-radius: 50%;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 14px;
            margin-right: 12px;
        }
        .user-code {
            background: #e7f3ff;
            border: 2px dashed #2196F3;
            border-radius: 8px;
            padding: 20px;
            text-align: center;
            margin-top: 10px;
        }
        .user-code-label {
            font-size: 12px;
            color: #666;
            text-transform: uppercase;
            letter-spacing: 1px;
            margin-bottom: 8px;
        }
        .user-code-value {
            font-size: 32px;
            font-weight: bold;
            font-family: monospace;
            color: #2196F3;
            letter-spacing: 4px;
        }
        .auth-btn {
            display: block;
            width: 100%;
            padding: 15px;
            background: #667eea;
            color: white;
            text-align: center;
            text-decoration: none;
            border-radius: 8px;
            font-weight: 600;
            font-size: 16px;
            transition: all 0.3s;
            border: none;
            cursor: pointer;
            margin-top: 20px;
        }
        .auth-btn:hover {
            background: #5568d3;
            transform: translateY(-2px);
            box-shadow: 0 4px 12px rgba(102, 126, 234, 0.4);
        }
        .status {
            margin-top: 30px;
            padding: 20px;
            background: #f8f9fa;
            border-radius: 8px;
            text-align: center;
        }
        .status-pending { border-left: 4px solid #ffc107; }
        .status-success { border-left: 4px solid #28a745; }
        .status-failed { border-left: 4px solid #dc3545; }
        .spinner {
            border: 3px solid #f3f3f3;
            border-top: 3px solid #667eea;
            border-radius: 50%;
            width: 40px;
            height: 40px;
            animation: spin 1s linear infinite;
            margin: 0 auto 15px;
        }
        @keyframes spin {
            0% { transform: rotate(0deg); }
            100% { transform: rotate(360deg); }
        }
        .timer {
            font-size: 24px;
            font-weight: bold;
            color: #667eea;
            margin: 10px 0;
        }
        .timer.warning { color: #ffc107; }
        .timer.danger { color: #dc3545; }
        .status-message { color: #666; line-height: 1.6; }
        .success-icon, .error-icon { font-size: 48px; margin-bottom: 15px; }
        .info-box {
            background: #e7f3ff;
            border-left: 4px solid #2196F3;
            padding: 15px;
            margin-top: 20px;
            border-radius: 4px;
            font-size: 14px;
            color: #666;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔐 AWS SSO Authentication</h1>
        <p class="subtitle">Follow the steps below to complete authentication</p>
        
        <div class="step">
            <div class="step-title">
                <span class="step-number">1</span>
                Click the button below to open the authorization page
            </div>
            <a href="{{.AuthURL}}" target="_blank" class="auth-btn" id="authBtn">
                🚀 Open Authorization Page
            </a>
        </div>
        
        <div class="step">
            <div class="step-title">
                <span class="step-number">2</span>
                Enter the verification code below
            </div>
            <div class="user-code">
                <div class="user-code-label">Verification Code</div>
                <div class="user-code-value">{{.UserCode}}</div>
            </div>
        </div>
        
        <div class="step">
            <div class="step-title">
                <span class="step-number">3</span>
                Complete AWS SSO login
            </div>
            <p style="color: #666; font-size: 14px; margin-top: 10px;">
                Use your AWS SSO account to login and authorize
            </p>
        </div>
        
        <div class="status status-pending" id="statusBox">
            <div class="spinner" id="spinner"></div>
            <div class="timer" id="timer">{{.ExpiresIn}}s</div>
            <div class="status-message" id="statusMessage">
                Waiting for authorization...
            </div>
        </div>
        
        <div class="info-box">
            💡 <strong>Tip:</strong> The authorization page will open in a new tab. This page will automatically update once authorization is complete.
        </div>
    </div>
    
    <script>
        let pollInterval;
        let timerInterval;
        let remainingSeconds = {{.ExpiresIn}};
        const stateID = "{{.StateID}}";
        
        setTimeout(() => {
            document.getElementById('authBtn').click();
        }, 500);
        
        function pollStatus() {
            fetch('/v0/oauth/kiro/status?state=' + stateID)
                .then(response => response.json())
                .then(data => {
                    console.log('Status:', data);
                    if (data.status === 'success') {
                        clearInterval(pollInterval);
                        clearInterval(timerInterval);
                        showSuccess(data);
                    } else if (data.status === 'failed') {
                        clearInterval(pollInterval);
                        clearInterval(timerInterval);
                        showError(data);
                    } else {
                        remainingSeconds = data.remaining_seconds || 0;
                    }
                })
                .catch(error => {
                    console.error('Poll error:', error);
                });
        }
        
        function updateTimer() {
            const timerEl = document.getElementById('timer');
            const minutes = Math.floor(remainingSeconds / 60);
            const seconds = remainingSeconds % 60;
            timerEl.textContent = minutes + ':' + seconds.toString().padStart(2, '0');
            
            if (remainingSeconds < 60) {
                timerEl.className = 'timer danger';
            } else if (remainingSeconds < 180) {
                timerEl.className = 'timer warning';
            } else {
                timerEl.className = 'timer';
            }
            
            remainingSeconds--;
            
            if (remainingSeconds < 0) {
                clearInterval(timerInterval);
                clearInterval(pollInterval);
                showError({ error: 'Authentication timed out. Please refresh and try again.' });
            }
        }
        
        function showSuccess(data) {
            const statusBox = document.getElementById('statusBox');
            statusBox.className = 'status status-success';
            statusBox.innerHTML = '<div class="success-icon">✅</div>' +
                '<div class="status-message">' +
                '<strong>Authentication Successful!</strong><br>' +
                'Token expires: ' + new Date(data.expires_at).toLocaleString() +
                '</div>';
        }
        
        function showError(data) {
            const statusBox = document.getElementById('statusBox');
            statusBox.className = 'status status-failed';
            statusBox.innerHTML = '<div class="error-icon">❌</div>' +
                '<div class="status-message">' +
                '<strong>Authentication Failed</strong><br>' +
                (data.error || 'Unknown error') +
                '</div>' +
                '<button class="auth-btn" onclick="location.reload()" style="margin-top: 15px;">' +
                '🔄 Retry' +
                '</button>';
        }
        
        pollInterval = setInterval(pollStatus, 3000);
        timerInterval = setInterval(updateTimer, 1000);
        pollStatus();
    </script>
</body>
</html>`

	oauthWebErrorPageHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Failed</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            max-width: 600px;
            margin: 50px auto;
            padding: 20px;
            background: #f5f5f5;
        }
        .error {
            background: #fff;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            border-left: 4px solid #dc3545;
        }
        h1 { color: #dc3545; margin-top: 0; }
        .error-message { color: #666; line-height: 1.6; }
        .retry-btn {
            display: inline-block;
            margin-top: 20px;
            padding: 10px 20px;
            background: #007bff;
            color: white;
            text-decoration: none;
            border-radius: 4px;
        }
        .retry-btn:hover { background: #0056b3; }
    </style>
</head>
<body>
    <div class="error">
        <h1>❌ Authentication Failed</h1>
        <div class="error-message">
            <p><strong>Error:</strong></p>
            <p>{{.Error}}</p>
        </div>
        <a href="/v0/oauth/kiro/start" class="retry-btn">🔄 Retry</a>
    </div>
</body>
</html>`

	oauthWebSuccessPageHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            max-width: 600px;
            margin: 50px auto;
            padding: 20px;
            background: #f5f5f5;
        }
        .success {
            background: #fff;
            padding: 30px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            border-left: 4px solid #28a745;
            text-align: center;
        }
        h1 { color: #28a745; margin-top: 0; }
        .success-message { color: #666; line-height: 1.6; }
        .icon { font-size: 48px; margin-bottom: 15px; }
        .expires { font-size: 14px; color: #999; margin-top: 15px; }
    </style>
</head>
<body>
    <div class="success">
        <div class="icon">✅</div>
        <h1>Authentication Successful!</h1>
        <div class="success-message">
            <p>You can close this window.</p>
        </div>
        <div class="expires">Token expires: {{.ExpiresAt}}</div>
    </div>
</body>
</html>`

	oauthWebSelectPageHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Select Authentication Method</title>
    <style>
        * { box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            margin: 0;
            padding: 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
        }
        .container {
            max-width: 500px;
            width: 100%;
            background: #fff;
            padding: 40px;
            border-radius: 12px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.2);
        }
        h1 {
            margin: 0 0 10px;
            color: #333;
            font-size: 24px;
            text-align: center;
        }
        .subtitle {
            text-align: center;
            color: #666;
            margin-bottom: 30px;
        }
        .auth-methods {
            display: flex;
            flex-direction: column;
            gap: 15px;
        }
        .auth-btn {
            display: flex;
            align-items: center;
            width: 100%;
            padding: 15px 20px;
            background: #667eea;
            color: white;
            text-decoration: none;
            border-radius: 8px;
            font-weight: 600;
            font-size: 16px;
            transition: all 0.3s;
            border: none;
            cursor: pointer;
        }
        .auth-btn:hover {
            background: #5568d3;
            transform: translateY(-2px);
            box-shadow: 0 4px 12px rgba(102, 126, 234, 0.4);
        }
        .auth-btn .icon {
            font-size: 24px;
            margin-right: 15px;
            width: 32px;
            text-align: center;
        }
        .auth-btn.google { background: #4285F4; }
        .auth-btn.google:hover { background: #3367D6; }
        .auth-btn.github { background: #24292e; }
        .auth-btn.github:hover { background: #1a1e22; }
        .auth-btn.aws { background: #FF9900; }
        .auth-btn.aws:hover { background: #E68A00; }
        .auth-btn.idc { background: #232F3E; }
        .auth-btn.idc:hover { background: #1a242f; }
        .idc-form {
            background: #f8f9fa;
            padding: 20px;
            border-radius: 8px;
            margin-top: 15px;
            display: none;
        }
        .idc-form.show {
            display: block;
        }
        .form-group {
            margin-bottom: 15px;
        }
        .form-group label {
            display: block;
            font-weight: 600;
            color: #333;
            margin-bottom: 8px;
            font-size: 14px;
        }
        .form-group input {
            width: 100%;
            padding: 12px;
            border: 2px solid #e0e0e0;
            border-radius: 6px;
            font-size: 14px;
            transition: border-color 0.3s;
        }
        .form-group input:focus {
            outline: none;
            border-color: #667eea;
        }
        .form-group .hint {
            font-size: 12px;
            color: #999;
            margin-top: 5px;
        }
        .submit-btn {
            display: block;
            width: 100%;
            padding: 15px;
            background: #232F3E;
            color: white;
            text-align: center;
            text-decoration: none;
            border-radius: 8px;
            font-weight: 600;
            font-size: 16px;
            transition: all 0.3s;
            border: none;
            cursor: pointer;
        }
        .submit-btn:hover {
            background: #1a242f;
            transform: translateY(-2px);
            box-shadow: 0 4px 12px rgba(35, 47, 62, 0.4);
        }
        .divider {
            display: flex;
            align-items: center;
            margin: 20px 0;
        }
        .divider::before,
        .divider::after {
            content: "";
            flex: 1;
            border-bottom: 1px solid #e0e0e0;
        }
        .divider span {
            padding: 0 15px;
            color: #999;
            font-size: 14px;
        }
        .info-box {
            background: #e7f3ff;
            border-left: 4px solid #2196F3;
            padding: 15px;
            margin-top: 20px;
            border-radius: 4px;
            font-size: 14px;
            color: #666;
        }
        .warning-box {
            background: #fff3cd;
            border-left: 4px solid #ffc107;
            padding: 15px;
            margin-top: 20px;
            border-radius: 4px;
            font-size: 14px;
            color: #856404;
        }
        .auth-btn.manual { background: #6c757d; }
        .auth-btn.manual:hover { background: #5a6268; }
        .auth-btn.refresh { background: #17a2b8; }
        .auth-btn.refresh:hover { background: #138496; }
        .auth-btn.refresh:disabled { background: #7fb3bd; cursor: not-allowed; }
        .manual-form {
            background: #f8f9fa;
            padding: 20px;
            border-radius: 8px;
            margin-top: 15px;
            display: none;
        }
        .manual-form.show {
            display: block;
        }
        .form-group textarea {
            width: 100%;
            padding: 12px;
            border: 2px solid #e0e0e0;
            border-radius: 6px;
            font-size: 14px;
            font-family: monospace;
            transition: border-color 0.3s;
            resize: vertical;
            min-height: 80px;
        }
        .form-group textarea:focus {
            outline: none;
            border-color: #667eea;
        }
        .status-message {
            padding: 15px;
            border-radius: 6px;
            margin-top: 15px;
            display: none;
        }
        .status-message.success {
            background: #d4edda;
            color: #155724;
            display: block;
        }
        .status-message.error {
            background: #f8d7da;
            color: #721c24;
            display: block;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🔐 Select Authentication Method</h1>
        <p class="subtitle">Choose how you want to authenticate with Kiro</p>
        
        <div class="auth-methods">
            <a href="/v0/oauth/kiro/start?method=builder-id" class="auth-btn aws">
                <span class="icon">🔶</span>
                AWS Builder ID (Recommended)
            </a>
            
            <button type="button" class="auth-btn idc" onclick="toggleIdcForm()">
                <span class="icon">🏢</span>
                AWS Identity Center (IDC)
            </button>
            
            <div class="divider"><span>or</span></div>
            
            <button type="button" class="auth-btn manual" onclick="toggleManualForm()">
                <span class="icon">📋</span>
                Import RefreshToken from Kiro IDE
            </button>
            
            <button type="button" class="auth-btn refresh" onclick="manualRefresh()" id="refreshBtn">
                <span class="icon">🔄</span>
                Manual Refresh All Tokens
            </button>
            
            <div class="status-message" id="refreshStatus"></div>
        </div>
        
        <div class="idc-form" id="idcForm">
            <form action="/v0/oauth/kiro/start" method="get">
                <input type="hidden" name="method" value="idc">
                
                <div class="form-group">
                    <label for="startUrl">Start URL</label>
                    <input type="url" id="startUrl" name="startUrl" placeholder="https://your-org.awsapps.com/start" required>
                    <div class="hint">Your AWS Identity Center Start URL</div>
                </div>
                
                <div class="form-group">
                    <label for="region">Region</label>
                    <input type="text" id="region" name="region" value="us-east-1" placeholder="us-east-1">
                    <div class="hint">AWS Region for your Identity Center</div>
                </div>
                
                <button type="submit" class="submit-btn">
                    🚀 Continue with IDC
                </button>
            </form>
        </div>
        
        <div class="manual-form" id="manualForm">
            <form id="importForm" onsubmit="submitImport(event)">
                <div class="form-group">
                    <label for="refreshToken">Refresh Token</label>
                    <textarea id="refreshToken" name="refreshToken" placeholder="Paste your refreshToken here (starts with aorAAAAAG...)" required></textarea>
                    <div class="hint">Copy from Kiro IDE: ~/.kiro/kiro-auth-token.json → refreshToken field</div>
                </div>
                
                <button type="submit" class="submit-btn" id="importBtn">
                    📥 Import Token
                </button>
                
                <div class="status-message" id="importStatus"></div>
            </form>
        </div>
        
        <div class="warning-box">
            ⚠️ <strong>Note:</strong> Google and GitHub login are not available for third-party applications due to AWS Cognito restrictions. Please use AWS Builder ID or import your token from Kiro IDE.
        </div>
        
        <div class="info-box">
            💡 <strong>How to get RefreshToken:</strong><br>
            1. Open Kiro IDE and login with Google/GitHub<br>
            2. Find the token file: <code>~/.kiro/kiro-auth-token.json</code><br>
            3. Copy the <code>refreshToken</code> value and paste it above
        </div>
    </div>
    
    <script>
        function toggleIdcForm() {
            const idcForm = document.getElementById('idcForm');
            const manualForm = document.getElementById('manualForm');
            manualForm.classList.remove('show');
            idcForm.classList.toggle('show');
            if (idcForm.classList.contains('show')) {
                document.getElementById('startUrl').focus();
            }
        }
        
        function toggleManualForm() {
            const idcForm = document.getElementById('idcForm');
            const manualForm = document.getElementById('manualForm');
            idcForm.classList.remove('show');
            manualForm.classList.toggle('show');
            if (manualForm.classList.contains('show')) {
                document.getElementById('refreshToken').focus();
            }
        }
        
        async function submitImport(event) {
            event.preventDefault();
            const refreshToken = document.getElementById('refreshToken').value.trim();
            const statusEl = document.getElementById('importStatus');
            const btn = document.getElementById('importBtn');
            
            if (!refreshToken) {
                statusEl.className = 'status-message error';
                statusEl.textContent = 'Please enter a refresh token';
                return;
            }
            
            if (!refreshToken.startsWith('aorAAAAAG')) {
                statusEl.className = 'status-message error';
                statusEl.textContent = 'Invalid token format. Token should start with aorAAAAAG...';
                return;
            }
            
            btn.disabled = true;
            btn.textContent = '⏳ Importing...';
            statusEl.className = 'status-message';
            statusEl.style.display = 'none';
            
            try {
                const response = await fetch('/v0/oauth/kiro/import', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ refreshToken: refreshToken })
                });
                
                const data = await response.json();
                
                if (response.ok && data.success) {
                    statusEl.className = 'status-message success';
                    statusEl.textContent = '✅ Token imported successfully! File: ' + (data.fileName || 'kiro-token.json');
                } else {
                    statusEl.className = 'status-message error';
                    statusEl.textContent = '❌ ' + (data.error || data.message || 'Import failed');
                }
            } catch (error) {
                statusEl.className = 'status-message error';
                statusEl.textContent = '❌ Network error: ' + error.message;
            } finally {
                btn.disabled = false;
                btn.textContent = '📥 Import Token';
            }
        }
        
        async function manualRefresh() {
            const btn = document.getElementById('refreshBtn');
            const statusEl = document.getElementById('refreshStatus');
            
            btn.disabled = true;
            btn.innerHTML = '<span class="icon">⏳</span> Refreshing...';
            statusEl.className = 'status-message';
            statusEl.style.display = 'none';
            
            try {
                const response = await fetch('/v0/oauth/kiro/refresh', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' }
                });
                
                const data = await response.json();
                
                if (response.ok && data.success) {
                    statusEl.className = 'status-message success';
                    let msg = '✅ ' + data.message;
                    if (data.warnings && data.warnings.length > 0) {
                        msg += ' (Warnings: ' + data.warnings.join('; ') + ')';
                    }
                    statusEl.textContent = msg;
                } else {
                    statusEl.className = 'status-message error';
                    statusEl.textContent = '❌ ' + (data.error || data.message || 'Refresh failed');
                }
            } catch (error) {
                statusEl.className = 'status-message error';
                statusEl.textContent = '❌ Network error: ' + error.message;
            } finally {
                btn.disabled = false;
                btn.innerHTML = '<span class="icon">🔄</span> Manual Refresh All Tokens';
            }
        }
    </script>
</body>
</html>`
)
