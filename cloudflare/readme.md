# Cloudflare Deployment Guide

> **Note on Prerequisites:** This deployment method relies on Cloudflare Workers Containers and Durable Objects, which require a **Cloudflare Paid Workers Plan** (currently $5/mo). However, the required R2 storage bucket falls well within the free tier (currently up to 10GB).

**Step 1: Install Cloudflare Wrangler**
```bash
npm install -g wrangler
```

**Step 2: Verify Installation**
```bash
wrangler --version
```

**Step 3: Log in to Cloudflare**
```bash
wrangler login
```

**Step 4: Configure Your Local Proxy Settings**
Before deploying, you must configure your personal management key and proxy settings. Copy the default config:
```bash
cp config.example.yaml config.yaml
```
Open `config.yaml` in your text editor and edit the following:
- `api_key`: (The API key you will use in clients like Cursor or your IDE to access the proxy)
- `manage_key`: (Your password for logging into the web management portal)
- Customize any other proxy settings you'd like to personalize.

**Step 5: Create Environment File**
```bash
cp .env.example .env
```

**Step 6: Configure R2 Storage**
You need to create an R2 Bucket in the Cloudflare Dashboard (e.g., named `cli-proxy-config`).
1. Go to **R2 -> Manage R2 API Tokens** and create a token with **Object Read & Write** permissions.
2. Note your **Account ID** (found on the right sidebar of the Cloudflare Dashboard).

Open `.env` in your text editor, uncomment the Cloudflare R2 section, and enter your credentials:
```env
# R2 Bucket Credentials
OBJECTSTORE_ENDPOINT=https://<your-account-id>.r2.cloudflarestorage.com
OBJECTSTORE_BUCKET=cli-proxy-config
OBJECTSTORE_ACCESS_KEY=<your_r2_access_key>
OBJECTSTORE_SECRET_KEY=<your_r2_secret_key>
```

**Step 7: Install Project Dependencies**
```bash
npm install
```

**Step 8: Deploy to Cloudflare**
```bash
wrangler deploy
```

**Step 9: Find Your URL & Access the Management Portal**
When `wrangler deploy` finishes running in the previous step, look closely at the very end of the terminal output. It will print out your new proxy's live URL (e.g., `https://cli-proxy-api.<your-subdomain>.workers.dev`). 

Open your browser and navigate to this URL. Log in using the `manage_key` you set in Step 4.

---

### Testing Guide

To verify your proxy is working correctly, open your terminal and send a test request. Replace `<your-subdomain>` and `<your-proxy-api-key>` with your actual values:

```bash
curl -s -X POST https://cli-proxy-api.<your-subdomain>.workers.dev/v1/chat/completions \
  -H "Authorization: Bearer <your-proxy-api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [
      {"role": "user", "content": "Hello! Say testing 1 2 3"}
    ]
  }'
```

If successful, you will receive a JSON response from the proxy confirming the connection.
