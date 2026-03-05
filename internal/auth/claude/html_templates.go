// Package claude provides authentication and token management functionality
// for Anthropic's Claude AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Claude API.
package claude

// LoginSuccessHtml is the HTML template displayed to users after successful OAuth authentication.
// This template provides a user-friendly success page with options to close the window
// or navigate to the Claude platform. It includes automatic window closing functionality
// and keyboard accessibility features.
const LoginSuccessHtml = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful - Claude</title>
    <link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='%2310b981'%3E%3Cpath d='M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z'/%3E%3C/svg%3E">
    <style>
        * {
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 1rem;
        }
        .container {
            text-align: center;
            background: white;
            padding: 2.5rem;
            border-radius: 12px;
            box-shadow: 0 10px 25px rgba(0,0,0,0.1);
            max-width: 480px;
            width: 100%;
            animation: slideIn 0.3s ease-out;
        }
        @keyframes slideIn {
            from {
                opacity: 0;
                transform: translateY(-20px);
            }
            to {
                opacity: 1;
                transform: translateY(0);
            }
        }
        .success-icon {
            width: 64px;
            height: 64px;
            margin: 0 auto 1.5rem;
            background: #10b981;
            border-radius: 50%;
            display: flex;
            align-items: center;
            justify-content: center;
            color: white;
            font-size: 2rem;
            font-weight: bold;
        }
        h1 {
            color: #1f2937;
            margin-bottom: 1rem;
            font-size: 1.75rem;
            font-weight: 600;
        }
        .subtitle {
            color: #6b7280;
            margin-bottom: 1.5rem;
            font-size: 1rem;
            line-height: 1.5;
        }
        .setup-notice {
            background: #fef3c7;
            border: 1px solid #f59e0b;
            border-radius: 6px;
            padding: 1rem;
            margin: 1rem 0;
        }
        .setup-notice h3 {
            color: #92400e;
            margin: 0 0 0.5rem 0;
            font-size: 1rem;
        }
        .setup-notice p {
            color: #92400e;
            margin: 0;
            font-size: 0.875rem;
        }
        .setup-notice a {
            color: #1d4ed8;
            text-decoration: none;
        }
        .setup-notice a:hover {
            text-decoration: underline;
        }
        .actions {
            display: flex;
            gap: 1rem;
            justify-content: center;
            flex-wrap: wrap;
            margin-top: 2rem;
        }
        .button {
            padding: 0.75rem 1.5rem;
            border-radius: 8px;
            font-size: 0.875rem;
            font-weight: 500;
            text-decoration: none;
            transition: all 0.2s;
            cursor: pointer;
            border: none;
            display: inline-flex;
            align-items: center;
            gap: 0.5rem;
        }
        .button-primary {
            background: #3b82f6;
            color: white;
        }
        .button-primary:hover {
            background: #2563eb;
            transform: translateY(-1px);
        }
        .button-secondary {
            background: #f3f4f6;
            color: #374151;
            border: 1px solid #d1d5db;
        }
        .button-secondary:hover {
            background: #e5e7eb;
        }
        .countdown {
            color: #9ca3af;
            font-size: 0.75rem;
            margin-top: 1rem;
        }
        .footer {
            margin-top: 2rem;
            padding-top: 1.5rem;
            border-top: 1px solid #e5e7eb;
            color: #9ca3af;
            font-size: 0.75rem;
        }
        .footer a {
            color: #3b82f6;
            text-decoration: none;
        }
        .footer a:hover {
            text-decoration: underline;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="success-icon">✓</div>
        <h1>Authentication Successful!</h1>
        <p class="subtitle">You have successfully authenticated with Claude. You can now close this window and return to your terminal to continue.</p>
        
        {{SETUP_NOTICE}}
        
        <div class="actions">
            <button class="button button-primary" onclick="window.close()">
                <span>Close Window</span>
            </button>
            <a href="{{PLATFORM_URL}}" target="_blank" class="button button-secondary">
                <span>Open Platform</span>
                <span>↗</span>
            </a>
        </div>
        
        <div class="countdown">
            This window will close automatically in <span id="countdown">10</span> seconds
        </div>
        
        <div class="footer">
            <p>Powered by <a href="https://chatgpt.com" target="_blank">ChatGPT</a></p>
        </div>
    </div>
    
    <script>
        let countdown = 10;
        const countdownElement = document.getElementById('countdown');
        
        const timer = setInterval(() => {
            countdown--;
            countdownElement.textContent = countdown;
            
            if (countdown <= 0) {
                clearInterval(timer);
                window.close();
            }
        }, 1000);
        
        // Close window when user presses Escape
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                window.close();
            }
        });
        
        // Focus the close button for keyboard accessibility
        document.querySelector('.button-primary').focus();
    </script>
</body>
</html>`

// SetupNoticeHtml is the HTML template for the setup notice section.
// This template is embedded within the success page to inform users about
// additional setup steps required to complete their Claude account configuration.
const SetupNoticeHtml = `
        <div class="setup-notice">
            <h3>Additional Setup Required</h3>
            <p>To complete your setup, please visit the <a href="{{PLATFORM_URL}}" target="_blank">Claude</a> to configure your account.</p>
        </div>`
