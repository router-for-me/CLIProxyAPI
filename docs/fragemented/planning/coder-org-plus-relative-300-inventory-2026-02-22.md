# Coder Ecosystem + Relative Research Inventory (300 Repositories)

## Scope

- Source: `https://github.com/orgs/coder/repositories`
- Additional relative set: top adjacent repos relevant to CLI agent tooling, MCP, proxying, session/control workflows, and LLM operations.
- Date: 2026-02-22 (UTC)
- Total covered: **300 repositories**
  - `coder` org work: **203**
  - Additional related repos: **97**

## Selection Method

1. Pull full org payload from `orgs/coder/repos` and normalize fields.
2. Capture full org metrics and ordered inventory.
3. Build external candidate set from MCP/agent/CLI/LLM search surfaces.
4. Filter relevance (`agent`, `mcp`, `claude`, `codex`, `llm`, `proxy`, `terminal`, `orchestration`, `workflow`, `agentic`, etc.).
5. Remove overlaps and archived entries.
6. Sort by signal (stars, freshness, relevance fit) and pick 97 non-overlapping external repos.

---

## Part 1: coder org complete inventory (203 repos)

Source table (generated from direct GitHub API extraction):

# Coder Org Repo Inventory (as of 2026-02-22T09:57:01Z)

**Total repos:** 203
**Active:** 184
**Archived:** 19
**Updated in last 365d:** 106

| idx | repo | stars | language | archived | updated_at | description |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | coder/code-server | 76331 | TypeScript | false | 2026-02-22T06:39:46Z | VS Code in the browser |
| 2 | coder/coder | 12286 | Go | false | 2026-02-22T07:15:27Z | Secure environments for developers and their agents |
| 3 | coder/sshcode | 5715 | Go | true | 2026-02-20T13:56:05Z | Run VS Code on any server over SSH. |
| 4 | coder/websocket | 4975 | Go | false | 2026-02-22T07:55:53Z | Minimal and idiomatic WebSocket library for Go |
| 5 | coder/claudecode.nvim | 2075 | Lua | false | 2026-02-22T06:30:23Z | 🧩 Claude Code Neovim IDE Extension |
| 6 | coder/ghostty-web | 1853 | TypeScript | false | 2026-02-22T09:52:41Z | Ghostty for the web with xterm.js API compatibility |
| 7 | coder/wush | 1413 | Go | false | 2026-02-18T11:01:01Z | simplest & fastest way to transfer files between computers via WireGuard |
| 8 | coder/agentapi | 1215 | Go | false | 2026-02-22T05:17:09Z | HTTP API for Claude Code, Goose, Aider, Gemini, Amp, and Codex |
| 9 | coder/mux | 1200 | TypeScript | false | 2026-02-22T09:15:41Z | A desktop app for isolated, parallel agentic development |
| 10 | coder/deploy-code-server | 980 | Shell | false | 2026-02-16T22:44:24Z | Deploy code-server to the cloud with a few clicks ☁️ 👨🏼‍💻 |
| 11 | coder/httpjail | 904 | Rust | false | 2026-02-17T18:03:11Z | HTTP(s) request filter for processes |
| 12 | coder/sail | 631 | Go | true | 2025-11-27T06:19:55Z | Deprecated: Instant, pre-configured VS Code development environments. |
| 13 | coder/slog | 348 | Go | false | 2026-01-28T15:15:48Z | Minimal structured logging library for Go |
| 14 | coder/code-marketplace | 341 | Go | false | 2026-02-09T10:27:27Z | Open source extension marketplace for VS Code. |
| 15 | coder/guts | 310 | Go | false | 2026-02-18T06:58:52Z | Guts is a code generator that converts Golang types to Typescript. Useful for keeping types in sync between the front and backend. |
| 16 | coder/envbuilder | 283 | Go | false | 2026-02-20T08:53:20Z | Build development environments from a Dockerfile on Docker, Kubernetes, and OpenShift. Enable developers to modify their development environment quickly. |
| 17 | coder/quartz | 271 | Go | false | 2026-02-16T15:58:44Z | A Go time testing library for writing deterministic unit tests |
| 18 | coder/anyclaude | 256 | TypeScript | false | 2026-02-19T20:10:01Z | Claude Code with any LLM |
| 19 | coder/picopilot | 254 | JavaScript | false | 2025-12-04T02:22:02Z | GitHub Copilot in 70 lines of JavaScript |
| 20 | coder/hnsw | 211 | Go | false | 2026-02-20T13:54:22Z | In-memory vector index for Go |
| 21 | coder/awesome-code-server | 191 |  | false | 2026-01-01T19:37:50Z | Projects, resources, and tutorials that take code-server to the next level |
| 22 | coder/awesome-coder | 191 |  | false | 2026-02-05T00:49:19Z | A curated list of awesome Coder resources. |
| 23 | coder/aicommit | 185 | Go | false | 2026-02-20T04:59:25Z | become the world's laziest committer |
| 24 | coder/redjet | 147 | Go | false | 2025-10-01T18:49:07Z | High-performance Redis library for Go |
| 25 | coder/images | 116 | Shell | false | 2026-02-03T13:54:55Z | Example Docker images for use with Coder |
| 26 | coder/vscode-coder | 115 | TypeScript | false | 2026-02-19T14:01:47Z | Open any Coder workspace in VS Code with a single click. |
| 27 | coder/nbin | 109 | TypeScript | true | 2025-09-16T15:43:49Z | Fast and robust node.js binary compiler. |
| 28 | coder/cursor-arm | 107 | Nix | true | 2026-02-04T16:26:31Z | Cursor built for ARM Linux and Windows |
| 29 | coder/blink | 104 | TypeScript | false | 2026-02-21T23:02:57Z | Blink is a self-hosted platform for building and running custom, in-house AI agents. |
| 30 | coder/pulldash | 103 | TypeScript | false | 2026-02-04T01:36:38Z | Review pull requests in a high-performance UI, driven by keybinds. |
| 31 | coder/acp-go-sdk | 78 | Go | false | 2026-02-19T11:19:38Z | Go SDK for the Agent Client Protocol (ACP), offering typed requests, responses, and helpers so Go applications can build ACP-compliant agents, clients, and integrations |
| 32 | coder/coder-v1-cli | 70 |  | true | 2025-08-02T15:09:07Z | Command line for Coder v1. For Coder v2, go to https://github.com/coder/coder |
| 33 | coder/balatrollm | 65 | Python | false | 2026-02-21T15:47:21Z | Play Balatro with LLMs 🎯 |
| 34 | coder/backstage-plugins | 64 | TypeScript | false | 2026-02-21T14:07:09Z | Official Coder plugins for the Backstage platform |
| 35 | coder/envbox | 61 | Go | false | 2026-02-04T03:21:32Z | envbox is an image that enables creating non-privileged containers capable of running system-level software (e.g. dockerd, systemd, etc) in Kubernetes. |
| 36 | coder/terraform-provider-coder | 54 | Go | false | 2026-02-10T09:20:24Z |  |
| 37 | coder/registry | 52 | HCL | false | 2026-02-18T16:14:55Z | Publish Coder modules and templates for other developers to use. |
| 38 | coder/cli | 50 | Go | true | 2025-03-03T05:37:28Z | A minimal Go CLI package. |
| 39 | coder/enterprise-helm | 49 | Go | false | 2026-01-10T08:31:06Z | Operate Coder v1 on Kubernetes |
| 40 | coder/modules | 48 | HCL | true | 2025-11-11T15:29:02Z | A collection of Terraform Modules to extend Coder templates. |
| 41 | coder/balatrobot | 46 | Python | false | 2026-02-21T22:58:46Z | API for developing Balatro bots 🃏 |
| 42 | coder/wgtunnel | 44 | Go | false | 2026-01-29T18:25:01Z | HTTP tunnels over Wireguard |
| 43 | coder/retry | 41 | Go | false | 2025-02-16T02:57:18Z | A tiny retry package for Go. |
| 44 | coder/hat | 39 | Go | false | 2025-03-03T05:34:56Z | HTTP API testing for Go |
| 45 | coder/aisdk-go | 37 | Go | false | 2026-02-13T19:37:52Z | A Go implementation of Vercel's AI SDK Data Stream Protocol. |
| 46 | coder/jetbrains-coder | 34 | Kotlin | false | 2026-01-21T21:41:12Z | A JetBrains Plugin for Coder Workspaces |
| 47 | coder/exectrace | 32 | Go | false | 2026-01-14T19:46:53Z | Simple eBPF-based exec snooping on Linux packaged as a Go library. |
| 48 | coder/ai-tokenizer | 31 | TypeScript | false | 2026-02-19T14:06:57Z | A faster than tiktoken tokenizer with first-class support for Vercel's AI SDK. |
| 49 | coder/observability | 30 | Go | false | 2026-01-29T16:04:00Z |  |
| 50 | coder/packages | 30 | HCL | false | 2026-02-16T07:15:10Z | Deploy Coder to your preferred cloud with a pre-built package. |
| 51 | coder/labeler | 29 | Go | false | 2025-08-04T02:46:59Z | A GitHub app that labels your issues for you |
| 52 | coder/wsep | 29 | Go | false | 2025-04-16T13:41:20Z | High performance command execution protocol |
| 53 | coder/coder-logstream-kube | 28 | Go | false | 2026-02-20T12:31:58Z | Stream Kubernetes Pod events to the Coder startup logs |
| 54 | coder/node-browser | 28 | TypeScript | true | 2025-03-03T05:33:54Z | Use Node in the browser. |
| 55 | coder/vscode | 27 | TypeScript | false | 2025-09-15T10:08:35Z | Fork of Visual Studio Code to aid code-server integration. Work in progress ⚠️  |
| 56 | coder/wush-action | 26 | Shell | false | 2025-12-09T02:38:39Z | SSH into GitHub Actions |
| 57 | coder/docs | 25 | Shell | true | 2025-08-18T18:20:13Z | Markdown content for Coder v1 Docs. |
| 58 | coder/coder-desktop-windows | 23 | C# | false | 2026-02-17T09:41:58Z | Coder Desktop application for Windows |
| 59 | coder/flog | 23 | Go | false | 2025-05-13T15:36:30Z | Pretty formatted log for Go |
| 60 | coder/aibridge | 22 | Go | false | 2026-02-20T12:54:28Z | Intercept AI requests, track usage, inject MCP tools centrally |
| 61 | coder/coder-desktop-macos | 22 | Swift | false | 2026-02-17T03:30:13Z | Coder Desktop application for macOS |
| 62 | coder/terraform-provider-coderd | 22 | Go | false | 2026-02-06T02:11:23Z | Manage a Coder deployment using Terraform |
| 63 | coder/serpent | 21 | Go | false | 2026-02-19T17:49:37Z | CLI framework for scale and configurability inspired by Cobra |
| 64 | coder/boundary | 19 | Go | false | 2026-02-20T21:52:51Z |  |
| 65 | coder/code-server-aur | 17 | Shell | false | 2026-01-26T23:33:42Z | code-server AUR package |
| 66 | coder/coder-jetbrains-toolbox | 16 | Kotlin | false | 2026-02-14T23:21:02Z | Coder plugin for remote development support in JetBrains Toolbox |
| 67 | coder/homebrew-coder | 15 | Ruby | false | 2026-02-12T20:53:01Z | Coder Homebrew Tap |
| 68 | coder/pretty | 14 | Go | false | 2025-02-16T02:57:53Z | TTY styles for Go |
| 69 | coder/balatrobench | 13 | Python | false | 2026-02-19T18:04:04Z | Benchmark LLMs' strategic performance in Balatro 📊 |
| 70 | coder/cloud-agent | 13 | Go | false | 2025-08-08T04:30:34Z | The agent for Coder Cloud |
| 71 | coder/requirefs | 13 | TypeScript | true | 2025-03-03T05:33:23Z | Create a readable and requirable file system from tars, zips, or a custom provider. |
| 72 | coder/ts-logger | 13 | TypeScript | false | 2025-02-21T15:51:39Z |  |
| 73 | coder/envbuilder-starter-devcontainer | 12 | Dockerfile | false | 2025-08-25T01:14:30Z | A sample project for getting started with devcontainer.json in envbuilder |
| 74 | coder/setup-action | 12 |  | false | 2025-12-10T15:24:32Z | Downloads and Configures Coder. |
| 75 | coder/terraform-provider-envbuilder | 12 | Go | false | 2026-02-04T03:21:05Z |  |
| 76 | coder/timer | 11 | Go | true | 2026-01-26T06:07:54Z | Accurately measure how long a command takes to run  |
| 77 | coder/webinars | 11 | HCL | false | 2025-08-19T17:05:35Z |  |
| 78 | coder/bigdur | 10 | Go | false | 2025-03-03T05:42:27Z | A Go package for parsing larger durations. |
| 79 | coder/coder.rs | 10 | Rust | false | 2025-07-03T16:00:35Z | [EXPERIMENTAL] Asynchronous Rust wrapper around the Coder Enterprise API |
| 80 | coder/devcontainer-features | 10 | Shell | false | 2026-02-18T13:09:58Z |  |
| 81 | coder/presskit | 10 |  | false | 2025-06-25T14:37:29Z | press kit and brand assets for Coder.com |
| 82 | coder/cla | 9 |  | false | 2026-02-20T14:00:39Z | The Coder Contributor License Agreement (CLA) |
| 83 | coder/clistat | 9 | Go | false | 2026-01-05T12:08:10Z | A Go library for measuring and reporting resource usage within cgroups and hosts |
| 84 | coder/ssh | 9 | Go | false | 2025-10-31T17:48:34Z | Easy SSH servers in Golang |
| 85 | coder/codercord | 8 | TypeScript | false | 2026-02-16T18:51:56Z | A Discord bot for our community server  |
| 86 | coder/community-templates | 8 | HCL | true | 2025-12-07T03:39:36Z | Unofficial templates for Coder for various platforms and cloud providers |
| 87 | coder/devcontainer-webinar | 8 | Shell | false | 2026-01-05T08:24:24Z | The Good, The Bad, And The Future of Dev Containers |
| 88 | coder/coder-doctor | 7 | Go | true | 2025-02-16T02:59:32Z | A preflight check tool for Coder |
| 89 | coder/jetbrains-backend-coder | 7 | Kotlin | false | 2026-01-14T19:56:28Z |  |
| 90 | coder/preview | 7 | Go | false | 2026-02-20T14:46:48Z | Template preview engine |
| 91 | coder/ai.coder.com | 6 | HCL | false | 2026-01-21T16:39:36Z | Coder's AI-Agent Demo Environment |
| 92 | coder/blogs | 6 | D2 | false | 2025-03-13T06:49:54Z | Content for coder.com/blog |
| 93 | coder/ghlabels | 6 | Go | false | 2025-03-03T05:40:54Z | A tool to synchronize labels on GitHub repositories sanely. |
| 94 | coder/nfy | 6 | Go | false | 2025-03-03T05:39:13Z | EXPERIMENTAL: Pumped up install scripts |
| 95 | coder/semhub | 6 | TypeScript | false | 2026-02-10T11:15:45Z |  |
| 96 | coder/.github | 5 |  | false | 2026-02-11T01:27:53Z |  |
| 97 | coder/gke-disk-cleanup | 5 | Go | false | 2025-03-03T05:34:24Z |  |
| 98 | coder/go-tools | 5 | Go | false | 2024-08-02T23:06:32Z | [mirror] Go Tools |
| 99 | coder/kaniko | 5 | Go | false | 2025-11-07T13:56:38Z | Build Container Images In Kubernetes |
| 100 | coder/starquery | 5 | Go | false | 2026-01-19T18:20:32Z | Query in near-realtime if a user has starred a GitHub repository. |
| 101 | coder/tailscale | 5 | Go | false | 2026-02-10T03:43:17Z | The easiest, most secure way to use WireGuard and 2FA. |
| 102 | coder/boundary-releases | 4 |  | false | 2026-01-14T19:51:57Z | A simple process isolator for Linux that provides lightweight isolation focused on AI and development environments. |
| 103 | coder/coder-xray | 4 | Go | true | 2026-01-14T19:56:28Z | JFrog XRay Integration |
| 104 | coder/enterprise-terraform | 4 | HCL | false | 2025-03-03T05:32:04Z | Terraform modules and examples for deploying Coder |
| 105 | coder/grip | 4 | Go | false | 2025-09-20T20:27:11Z | extensible logging and messaging framework for go processes.  |
| 106 | coder/mutagen | 4 | Go | false | 2025-05-01T02:07:53Z | Make remote development work with your local tools |
| 107 | coder/sail-aur | 4 | Shell | true | 2025-03-03T05:41:24Z | sail AUR package |
| 108 | coder/support-scripts | 4 | Shell | false | 2025-03-03T05:36:24Z | Things for Coder Customer Success. |
| 109 | coder/agent-client-protocol | 3 | Rust | false | 2026-02-17T09:29:51Z |  A protocol for connecting any editor to any agent |
| 110 | coder/awesome-terraform | 3 |  | false | 2025-02-18T21:26:09Z | Curated list of resources on HashiCorp's Terraform |
| 111 | coder/coder-docs-generator | 3 | TypeScript | false | 2025-03-03T05:29:10Z | Generates off-line docs for Coder Docs |
| 112 | coder/devcontainers-features | 3 |  | false | 2025-05-30T10:37:24Z | A collection of development container 'features' |
| 113 | coder/devcontainers.github.io | 3 |  | false | 2024-08-02T23:19:31Z | Web content for the development containers specification. |
| 114 | coder/gott | 3 | Go | false | 2025-03-03T05:41:52Z | go test timer |
| 115 | coder/homebrew-core | 3 | Ruby | false | 2025-04-04T03:56:04Z | 🍻 Default formulae for the missing package manager for macOS (or Linux) |
| 116 | coder/internal | 3 |  | false | 2026-02-06T05:54:41Z | Non-community issues related to coder/coder |
| 117 | coder/presentations | 3 |  | false | 2025-03-03T05:31:04Z | Talks and presentations related to Coder released under CC0 which permits remixing and reuse! |
| 118 | coder/start-workspace-action | 3 | TypeScript | false | 2026-01-14T19:45:56Z |  |
| 119 | coder/synology | 3 | Shell | false | 2025-03-03T05:30:37Z | a work in progress prototype |
| 120 | coder/templates | 3 | HCL | false | 2026-01-05T23:16:26Z | Repository for internal demo templates across our different environments |
| 121 | coder/wxnm | 3 | TypeScript | false | 2025-03-03T05:35:47Z | A library for providing TypeScript typed communication between your web extension and your native Node application using Native Messaging |
| 122 | coder/action-gcs-cache | 2 | TypeScript | false | 2024-08-02T23:19:07Z | Cache dependencies and build outputs in GitHub Actions |
| 123 | coder/autofix | 2 | JavaScript | false | 2024-08-02T23:19:37Z | Automatically fix all software bugs. |
| 124 | coder/awesome-vscode | 2 |  | false | 2025-07-07T18:07:32Z | 🎨 A curated list of delightful VS Code packages and resources. |
| 125 | coder/aws-efs-csi-pv-provisioner | 2 | Go | false | 2024-08-02T23:19:06Z | Dynamically provisions Persistent Volumes backed by a subdirectory on AWS EFS in response to Persistent Volume Claims in conjunction with the AWS EFS CSI driver |
| 126 | coder/coder-platformx-notifications | 2 | Python | false | 2026-01-14T19:39:55Z | Transform Coder webhooks to PlatformX events |
| 127 | coder/containers-test | 2 | Dockerfile | false | 2025-02-16T02:56:47Z | Container images compatible with Coder |
| 128 | coder/example-dotfiles | 2 |  | false | 2025-10-25T18:04:11Z |  |
| 129 | coder/feeltty | 2 | Go | false | 2025-03-03T05:31:32Z | Quantify the typing experience of a TTY  |
| 130 | coder/fluid-menu-bar-extra | 2 | Swift | false | 2025-07-31T04:59:08Z | 🖥️ A lightweight tool for building great menu bar extras with SwiftUI. |
| 131 | coder/gvisor | 2 | Go | false | 2025-01-15T16:10:44Z | Application Kernel for Containers |
| 132 | coder/linux | 2 |  | false | 2024-08-02T23:19:08Z | Linux kernel source tree |
| 133 | coder/merge-queue-test | 2 | Shell | false | 2025-02-15T04:50:36Z |  |
| 134 | coder/netns | 2 | Go | false | 2024-08-02T23:19:12Z | Runc hook (OCI compatible) for setting up default bridge networking for containers. |
| 135 | coder/pq | 2 | Go | false | 2025-09-23T05:53:41Z | Pure Go Postgres driver for database/sql |
| 136 | coder/runtime-tools | 2 | Go | false | 2024-08-02T23:06:39Z | OCI Runtime Tools |
| 137 | coder/sandbox-for-github | 2 |  | false | 2025-03-03T05:29:59Z | a sandpit for playing around with GitHub configuration stuff such as GitHub actions or issue templates |
| 138 | coder/sshcode-aur | 2 | Shell | true | 2025-03-03T05:40:22Z | sshcode AUR package |
| 139 | coder/v2-templates | 2 |  | true | 2025-08-18T18:20:11Z |  |
| 140 | coder/vscodium | 2 |  | false | 2024-08-02T23:19:34Z | binary releases of VS Code without MS branding/telemetry/licensing |
| 141 | coder/web-rdp-bridge | 2 |  | true | 2025-04-04T03:56:08Z | A fork of Devolutions Gateway designed to help bring Windows Web RDP support to Coder. |
| 142 | coder/yamux | 2 | Go | false | 2024-08-02T23:19:24Z | Golang connection multiplexing library |
| 143 | coder/aws-workshop-samples | 1 | Shell | false | 2026-01-14T19:46:52Z | Sample Coder CLI Scripts and Templates to aid in the delivery of AWS Workshops and Immersion Days |
| 144 | coder/boundary-proto | 1 | Makefile | false | 2026-01-27T17:59:50Z | IPC API for boundary & Coder workspace agent |
| 145 | coder/bubbletea | 1 | Go | false | 2025-04-16T23:16:25Z | A powerful little TUI framework 🏗 |
| 146 | coder/c4d-packer | 1 |  | false | 2024-08-02T23:19:32Z | VM images with Coder + Caddy for automatic TLS. |
| 147 | coder/cloud-hypervisor | 1 | Rust | false | 2024-08-02T23:06:40Z | A rust-vmm based cloud hypervisor |
| 148 | coder/coder-desktop-linux | 1 | C# | false | 2026-02-18T11:46:15Z | Coder Desktop application for Linux (experimental) |
| 149 | coder/coder-k8s | 1 | Go | false | 2026-02-20T11:58:41Z |  |
| 150 | coder/coder-oss-gke-tf | 1 |  | false | 2024-08-02T23:19:35Z | see upstream at https://github.com/ElliotG/coder-oss-gke-tf |
| 151 | coder/copenhagen_theme | 1 | Handlebars | false | 2025-06-30T18:17:45Z | The default theme for Zendesk Guide |
| 152 | coder/create-task-action | 1 | TypeScript | false | 2026-01-19T16:32:14Z |  |
| 153 | coder/diodb | 1 |  | false | 2024-08-02T23:19:27Z | Open-source vulnerability disclosure and bug bounty program database. |
| 154 | coder/do-marketplace-partners | 1 | Shell | false | 2024-08-02T23:06:38Z | Image validation, automation, and other tools for DigitalOcean Marketplace partners and Custom Image users |
| 155 | coder/drpc | 1 |  | false | 2024-08-02T23:19:31Z | drpc is a lightweight, drop-in replacement for gRPC |
| 156 | coder/glog | 1 | Go | false | 2024-08-02T23:19:18Z | Leveled execution logs for Go |
| 157 | coder/go-containerregistry | 1 |  | false | 2024-08-02T23:19:33Z | Go library and CLIs for working with container registries |
| 158 | coder/go-httpstat | 1 | Go | false | 2024-08-02T23:19:46Z | Tracing golang HTTP request latency |
| 159 | coder/go-scim | 1 | Go | false | 2024-08-02T23:19:40Z | Building blocks for servers implementing Simple Cloud Identity Management v2 |
| 160 | coder/gotestsum | 1 |  | false | 2024-08-02T23:19:37Z | 'go test' runner with output optimized for humans, JUnit XML for CI integration, and a summary of the test results. |
| 161 | coder/imdisk-artifacts | 1 | Batchfile | false | 2025-04-04T03:56:04Z |  |
| 162 | coder/infracost | 1 |  | false | 2024-08-02T23:19:26Z | Cloud cost estimates for Terraform in pull requests💰📉 Love your cloud bill! |
| 163 | coder/kcp-go | 1 | Go | false | 2024-08-02T23:19:21Z |  A Production-Grade Reliable-UDP Library for golang |
| 164 | coder/nixpkgs | 1 |  | false | 2024-08-02T23:19:30Z | Nix Packages collection |
| 165 | coder/oauth1 | 1 | Go | false | 2024-08-02T23:19:20Z | Go OAuth1 |
| 166 | coder/oauth2 | 1 | Go | false | 2024-08-02T23:19:10Z | Go OAuth2 |
| 167 | coder/pacman-nodejs | 1 |  | false | 2024-08-29T19:49:32Z |  |
| 168 | coder/paralleltestctx | 1 | Go | false | 2025-08-15T08:48:57Z | Go linter for finding usages of contexts with timeouts in parallel subtests. |
| 169 | coder/pnpm2nix-nzbr | 1 | Nix | false | 2025-04-04T03:56:05Z | Build packages using pnpm with nix |
| 170 | coder/rancher-partner-charts | 1 | Smarty | true | 2025-04-04T03:56:06Z | A catalog based on applications from independent software vendors (ISVs). Most of them are SUSE Partners. |
| 171 | coder/slack-autoarchive | 1 |  | false | 2024-08-02T23:19:10Z | If there has been no activity in a channel for awhile, you can automatically archive it using a cronjob. |
| 172 | coder/srecon-emea-2024 | 1 | HCL | false | 2025-04-04T03:56:07Z |  |
| 173 | coder/terraform-config-inspect | 1 | Go | false | 2025-10-25T18:04:07Z | A helper library for shallow inspection of Terraform configurations |
| 174 | coder/terraform-provider-docker | 1 |  | false | 2025-05-24T22:16:42Z | Terraform Docker provider |
| 175 | coder/uap-go | 1 |  | false | 2024-08-02T23:19:16Z | Go implementation of ua-parser |
| 176 | coder/wireguard-go | 1 | Go | false | 2024-08-02T23:19:22Z | Mirror only. Official repository is at https://git.zx2c4.com/wireguard-go |
| 177 | coder/actions-cache | 0 | TypeScript | false | 2025-04-22T12:16:39Z | Cache dependencies and build outputs in GitHub Actions |
| 178 | coder/afero | 0 | Go | false | 2025-12-12T18:24:29Z | The Universal Filesystem Abstraction for Go |
| 179 | coder/agentapi-sdk-go | 0 | Go | false | 2025-05-05T13:27:45Z |  |
| 180 | coder/agents.md | 0 | TypeScript | false | 2026-01-07T18:31:24Z | AGENTS.md — a simple, open format for guiding coding agents |
| 181 | coder/agentskills | 0 | Python | false | 2026-01-07T17:26:22Z | Specification and documentation for Agent Skills |
| 182 | coder/aws-coder-ai-builder-gitops | 0 | HCL | false | 2026-02-17T17:10:11Z | Coder Templates to support AWS AI Builder Lab Events |
| 183 | coder/aws-coder-workshop-gitops | 0 | HCL | false | 2026-01-06T22:45:08Z | AWS Coder Workshop GitOps flow for Coder Template Admin |
| 184 | coder/blink-starter | 0 | TypeScript | false | 2026-01-26T10:39:36Z |  |
| 185 | coder/coder-1 | 0 |  | false | 2025-11-03T11:28:16Z | Secure environments for developers and their agents |
| 186 | coder/coder-aur | 0 | Shell | false | 2025-05-05T15:24:57Z | coder AUR package |
| 187 | coder/defsec | 0 |  | false | 2025-01-17T20:36:57Z | Trivy's misconfiguration scanning engine |
| 188 | coder/embedded-postgres | 0 | Go | false | 2025-06-02T09:29:59Z | Run a real Postgres database locally on Linux, OSX or Windows as part of another Go application or test |
| 189 | coder/find-process | 0 |  | false | 2025-04-15T03:50:36Z | find process by port/pid/name etc. |
| 190 | coder/ghostty | 0 | Zig | false | 2025-11-12T15:02:36Z | 👻 Ghostty is a fast, feature-rich, and cross-platform terminal emulator that uses platform-native UI and GPU acceleration. |
| 191 | coder/large-module | 0 |  | false | 2025-06-16T14:51:00Z | A large terraform module, used for testing |
| 192 | coder/libbun-webkit | 0 |  | false | 2025-12-04T23:56:12Z | WebKit precompiled for libbun |
| 193 | coder/litellm | 0 |  | false | 2025-12-18T15:46:54Z | Python SDK, Proxy Server (AI Gateway) to call 100+ LLM APIs in OpenAI (or native) format, with cost tracking, guardrails, loadbalancing and logging. [Bedrock, Azure, OpenAI, VertexAI, Cohere, Anthropic, Sagemaker, HuggingFace, VLLM, NVIDIA NIM] |
| 194 | coder/mux-aur | 0 | Shell | false | 2026-02-09T19:56:19Z | mux AUR package |
| 195 | coder/parameters-playground | 0 | TypeScript | false | 2026-02-05T15:55:03Z |  |
| 196 | coder/python-project | 0 |  | false | 2024-10-17T18:26:12Z | Develop a Python project using devcontainers! |
| 197 | coder/rehype-github-coder | 0 |  | false | 2025-07-02T17:54:07Z | rehype plugins that match how GitHub transforms markdown on their site |
| 198 | coder/setup-ramdisk-action | 0 |  | false | 2025-05-27T10:19:47Z |  |
| 199 | coder/shared-docs-kb | 0 |  | false | 2025-05-21T17:04:04Z |  |
| 200 | coder/sqlc | 0 | Go | false | 2025-10-29T12:20:02Z | Generate type-safe code from SQL |
| 201 | coder/Subprocess | 0 | Swift | false | 2025-07-29T10:03:41Z | Swift library for macOS providing interfaces for both synchronous and asynchronous process execution |
| 202 | coder/trivy | 0 | Go | false | 2025-08-07T20:59:15Z | Find vulnerabilities, misconfigurations, secrets, SBOM in containers, Kubernetes, code repositories, clouds and more |
| 203 | coder/vscode- | 0 |  | false | 2025-10-24T08:20:11Z | Visual Studio Code |

---

## Part 2: Additional relative repositories (97)

# Additional Relative Repo Additions (97 repos)

**As of:** 2026-02-22T09:57:28Z

**Purpose:** Non-coder ecosystem repos relevant to coding-agent infrastructure, MCP, CLI automation, proxying, and terminal workflows, selected from top relevance pool.

**Selection method:**
- Seeded from GitHub search across MCP/agent/CLI/terminal/LLM topics.
- Sorted by stars.
- Excluded the prior 60-repo overlap set and coder org repos.
- Kept active-only entries.

| idx | repo | stars | language | updated_at | topics | description |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `n8n-io/n8n` | 175742 | TypeScript | 2026-02-22T09:51:45Z | ai,apis,automation,cli,data-flow,development,integration-framework,integrations,ipaas,low-code,low-code-platform,mcp,mcp-client,mcp-server,n8n,no-code,self-hosted,typescript,workflow,workflow-automation | Fair-code workflow automation platform with native AI capabilities. Combine visual building with custom code, self-host or cloud, 400+ integrations. |
| 2 | `google-gemini/gemini-cli` | 95248 | TypeScript | 2026-02-22T09:55:20Z | ai,ai-agents,cli,gemini,gemini-api,mcp-client,mcp-server | An open-source AI agent that brings the power of Gemini directly into your terminal. |
| 3 | `punkpeye/awesome-mcp-servers` | 81317 |  | 2026-02-22T09:44:56Z | ai,mcp | A collection of MCP servers. |
| 4 | `jesseduffield/lazygit` | 72824 | Go | 2026-02-22T09:10:46Z | cli,git,terminal | simple terminal UI for git commands |
| 5 | `Mintplex-Labs/anything-llm` | 54841 | JavaScript | 2026-02-22T09:48:00Z | ai-agents,custom-ai-agents,deepseek,kimi,llama3,llm,lmstudio,local-llm,localai,mcp,mcp-servers,moonshot,multimodal,no-code,ollama,qwen3,rag,vector-database,web-scraping | The all-in-one Desktop & Docker AI application with built-in RAG, AI agents, No-code agent builder, MCP compatibility,  and more. |
| 6 | `affaan-m/everything-claude-code` | 49255 | JavaScript | 2026-02-22T09:51:52Z | ai-agents,anthropic,claude,claude-code,developer-tools,llm,mcp,productivity | Complete Claude Code configuration collection - agents, skills, hooks, commands, rules, MCPs. Battle-tested configs from an Anthropic hackathon winner. |
| 7 | `sansan0/TrendRadar` | 46836 | Python | 2026-02-22T09:41:02Z | ai,bark,data-analysis,docker,hot-news,llm,mail,mcp,mcp-server,news,ntfy,python,rss,trending-topics,wechat,wework | ⭐AI-driven public opinion & trend monitor with multi-platform aggregation, RSS, and smart alerts.🎯 告别信息过载，你的 AI 舆情监控助手与热点筛选工具！聚合多平台热点 +  RSS 订阅，支持关键词精准筛选。AI 翻译 +  AI 分析简报直推手机，也支持接入 MCP 架构，赋能 AI 自然语言对话分析、情感洞察与趋势预测等。支持 Docker ，数据本地/云端自持。集成微信/飞书/钉钉/Telegram/邮件/ntfy/bark/slack 等渠道智能推送。 |
| 8 | `upstash/context7` | 46464 | TypeScript | 2026-02-22T09:40:57Z | llm,mcp,mcp-server,vibe-coding | Context7 MCP Server -- Up-to-date code documentation for LLMs and AI code editors |
| 9 | `crewAIInc/crewAI` | 44427 | Python | 2026-02-22T09:40:04Z | agents,ai,ai-agents,aiagentframework,llms | Framework for orchestrating role-playing, autonomous AI agents. By fostering collaborative intelligence, CrewAI empowers agents to work together seamlessly, tackling complex tasks. |
| 10 | `spf13/cobra` | 43280 | Go | 2026-02-22T05:44:11Z | cli,cli-app,cobra,cobra-generator,cobra-library,command,command-cobra,command-line,commandline,go,golang,golang-application,golang-library,posix,posix-compliant-flags,subcommands | A Commander for modern Go CLI interactions |
| 11 | `mudler/LocalAI` | 42970 | Go | 2026-02-22T09:51:33Z | ai,api,audio-generation,decentralized,distributed,gemma,image-generation,libp2p,llama,llm,mamba,mcp,mistral,musicgen,object-detection,rerank,rwkv,stable-diffusion,text-generation,tts | :robot: The free, Open Source alternative to OpenAI, Claude and others. Self-hosted and local-first. Drop-in replacement,  running on consumer-grade hardware. No GPU required. Runs gguf, transformers, diffusers and many more. Features: Generate Text, MCP, Audio, Video, Images, Voice Cloning, Distributed, P2P and decentralized inference |
| 12 | `zhayujie/chatgpt-on-wechat` | 41359 | Python | 2026-02-22T09:41:37Z | ai,ai-agent,chatgpt,claude,deepseek,dingtalk,feishu-bot,gemini,kimi,linkai,llm,mcp,multi-agent,openai,openclaw,python3,qwen,skills,wechat | CowAgent是基于大模型的超级AI助理，能主动思考和任务规划、访问操作系统和外部资源、创造和执行Skills、拥有长期记忆并不断成长。同时支持飞书、钉钉、企业微信应用、微信公众号、网页等接入，可选择OpenAI/Claude/Gemini/DeepSeek/ Qwen/GLM/Kimi/LinkAI，能处理文本、语音、图片和文件，可快速搭建个人AI助手和企业数字员工。 |
| 13 | `Aider-AI/aider` | 40824 | Python | 2026-02-22T09:42:37Z | anthropic,chatgpt,claude-3,cli,command-line,gemini,gpt-3,gpt-35-turbo,gpt-4,gpt-4o,llama,openai,sonnet | aider is AI pair programming in your terminal |
| 14 | `mindsdb/mindsdb` | 38552 | Python | 2026-02-22T08:41:33Z | agents,ai,analytics,artificial-inteligence,bigquery,business-intelligence,databases,hacktoberfest,llms,mcp,mssql,mysql,postgresql,rag | Federated Query Engine for AI - The only MCP Server you'll ever need |
| 15 | `httpie/cli` | 37582 | Python | 2026-02-22T00:53:03Z | api,api-client,api-testing,cli,client,curl,debugging,developer-tools,development,devops,http,http-client,httpie,json,python,rest,rest-api,terminal,usability,web | 🥧 HTTPie CLI  — modern, user-friendly command-line HTTP client for the API era. JSON support, colors, sessions, downloads, plugins & more. |
| 16 | `ComposioHQ/awesome-claude-skills` | 36577 | Python | 2026-02-22T09:51:39Z | agent-skills,ai-agents,antigravity,automation,claude,claude-code,codex,composio,cursor,gemini-cli,mcp,rube,saas,skill,workflow-automation | A curated list of awesome Claude Skills, resources, and tools for customizing Claude AI workflows |
| 17 | `BerriAI/litellm` | 36541 | Python | 2026-02-22T09:46:04Z | ai-gateway,anthropic,azure-openai,bedrock,gateway,langchain,litellm,llm,llm-gateway,llmops,mcp-gateway,openai,openai-proxy,vertex-ai | Python SDK, Proxy Server (AI Gateway) to call 100+ LLM APIs in OpenAI (or native) format, with cost tracking, guardrails, loadbalancing and logging. [Bedrock, Azure, OpenAI, VertexAI, Cohere, Anthropic, Sagemaker, HuggingFace, VLLM, NVIDIA NIM] |
| 18 | `Textualize/textual` | 34404 | Python | 2026-02-22T09:36:12Z | cli,framework,python,rich,terminal,tui | The lean application framework for Python.  Build sophisticated user interfaces with a simple Python API. Run your apps in the terminal and a web browser. |
| 19 | `danny-avila/LibreChat` | 34022 | TypeScript | 2026-02-22T09:18:37Z | ai,anthropic,artifacts,aws,azure,chatgpt,chatgpt-clone,claude,clone,deepseek,gemini,google,gpt-5,librechat,mcp,o1,openai,responses-api,vision,webui | Enhanced ChatGPT Clone: Features Agents, MCP, DeepSeek, Anthropic, AWS, OpenAI, Responses API, Azure, Groq, o1, GPT-5, Mistral, OpenRouter, Vertex AI, Gemini, Artifacts, AI model switching, message search, Code Interpreter, langchain, DALL-E-3, OpenAPI Actions, Functions, Secure Multi-User Auth, Presets, open-source for self-hosting. Active. |
| 20 | `sxyazi/yazi` | 32994 | Rust | 2026-02-22T09:27:35Z | android,asyncio,cli,command-line,concurrency,cross-platform,developer-tools,file-explorer,file-manager,filesystem,linux,macos,neovim,productivity,rust,terminal,tui,vim,windows | 💥 Blazing fast terminal file manager written in Rust, based on async I/O. |
| 21 | `code-yeongyu/oh-my-opencode` | 32946 | TypeScript | 2026-02-22T09:54:53Z | ai,ai-agents,amp,anthropic,chatgpt,claude,claude-code,claude-skills,cursor,gemini,ide,openai,opencode,orchestration,tui,typescript | the best agent harness |
| 22 | `PDFMathTranslate/PDFMathTranslate` | 31852 | Python | 2026-02-22T09:12:58Z | chinese,document,edit,english,japanese,korean,latex,math,mcp,modify,obsidian,openai,pdf,pdf2zh,python,russian,translate,translation,zotero | [EMNLP 2025 Demo] PDF scientific paper translation with preserved formats - 基于 AI 完整保留排版的 PDF 文档全文双语翻译，支持 Google/DeepL/Ollama/OpenAI 等服务，提供 CLI/GUI/MCP/Docker/Zotero |
| 23 | `conductor-oss/conductor` | 31489 | Java | 2026-02-22T09:16:39Z | distributed-systems,durable-execution,grpc,java,javascript,microservice-orchestration,orchestration-engine,orchestrator,reactjs,spring-boot,workflow-automation,workflow-engine,workflow-management,workflows | Conductor is an event driven agentic orchestration platform providing durable and highly resilient execution engine for applications and AI Agents |
| 24 | `tqdm/tqdm` | 30973 | Python | 2026-02-22T09:13:13Z | cli,closember,console,discord,gui,jupyter,keras,meter,pandas,parallel,progress,progress-bar,progressbar,progressmeter,python,rate,telegram,terminal,time,utilities | :zap: A Fast, Extensible Progress Bar for Python and CLI |
| 25 | `block/goose` | 30888 | Rust | 2026-02-22T09:23:53Z | mcp | an open source, extensible AI agent that goes beyond code suggestions - install, execute, edit, and test with any LLM |
| 26 | `patchy631/ai-engineering-hub` | 30407 | Jupyter Notebook | 2026-02-22T09:33:50Z | agents,ai,llms,machine-learning,mcp,rag | In-depth tutorials on LLMs, RAGs and real-world AI agent applications. |
| 27 | `thedotmack/claude-mem` | 30047 | TypeScript | 2026-02-22T09:48:28Z | ai,ai-agents,ai-memory,anthropic,artificial-intelligence,chromadb,claude,claude-agent-sdk,claude-agents,claude-code,claude-code-plugin,claude-skills,embeddings,long-term-memory,mem0,memory-engine,openmemory,rag,sqlite,supermemory | A Claude Code plugin that automatically captures everything Claude does during your coding sessions, compresses it with AI (using Claude's agent-sdk), and injects relevant context back into future sessions. |
| 28 | `wshobson/agents` | 29088 | Python | 2026-02-22T09:49:48Z | agents,anthropic,anthropic-claude,automation,claude,claude-code,claude-code-cli,claude-code-commands,claude-code-plugin,claude-code-plugins,claude-code-skills,claude-code-subagents,claude-skills,claudecode,claudecode-config,claudecode-subagents,orchestration,sub-agents,subagents,workflows | Intelligent automation and multi-agent orchestration for Claude Code |
| 29 | `nrwl/nx` | 28185 | TypeScript | 2026-02-22T07:47:27Z | angular,build,build-system,build-tool,building-tool,cli,cypress,hacktoberfest,javascript,monorepo,nextjs,nodejs,nx,nx-workspaces,react,storybook,typescript | The Monorepo Platform that amplifies both developers and AI agents. Nx optimizes your builds, scales your CI, and fixes failed PRs automatically. Ship in half the time. |
| 30 | `google/python-fire` | 28130 | Python | 2026-02-22T09:13:41Z | cli,python | Python Fire is a library for automatically generating command line interfaces (CLIs) from absolutely any Python object. |
| 31 | `microsoft/playwright-mcp` | 27492 | TypeScript | 2026-02-22T09:03:03Z | mcp,playwright | Playwright MCP server |
| 32 | `github/github-mcp-server` | 27134 | Go | 2026-02-22T09:52:34Z | github,mcp,mcp-server | GitHub's official MCP Server |
| 33 | `ComposioHQ/composio` | 27111 | TypeScript | 2026-02-22T09:18:05Z | agentic-ai,agents,ai,ai-agents,aiagents,developer-tools,function-calling,gpt-4,javascript,js,llm,llmops,mcp,python,remote-mcp-server,sse,typescript | Composio powers 1000+ toolkits, tool search, context management, authentication, and a sandboxed workbench to help you build AI agents that turn intent into action. |
| 34 | `angular/angular-cli` | 27029 | TypeScript | 2026-02-21T09:44:49Z | angular,angular-cli,cli,typescript | CLI tool for Angular |
| 35 | `simstudioai/sim` | 26509 | TypeScript | 2026-02-22T08:54:59Z | agent-workflow,agentic-workflow,agents,ai,aiagents,anthropic,artificial-intelligence,automation,chatbot,deepseek,gemini,low-code,nextjs,no-code,openai,rag,react,typescript | Build, deploy, and orchestrate AI agents. Sim is the central intelligence layer for your AI workforce. |
| 36 | `ChromeDevTools/chrome-devtools-mcp` | 26353 | TypeScript | 2026-02-22T09:55:22Z | browser,chrome,chrome-devtools,debugging,devtools,mcp,mcp-server,puppeteer | Chrome DevTools for coding agents |
| 37 | `Fosowl/agenticSeek` | 25088 | Python | 2026-02-22T08:26:23Z | agentic-ai,agents,ai,autonomous-agents,deepseek-r1,llm,llm-agents,voice-assistant | Fully Local Manus AI. No APIs, No $200 monthly bills. Enjoy an autonomous agent that thinks, browses the web, and code for the sole cost of electricity. 🔔 Official updates only via twitter @Martin993886460 (Beware of fake account) |
| 38 | `withfig/autocomplete` | 25071 | TypeScript | 2026-02-21T03:23:10Z | autocomplete,bash,cli,fig,fish,hacktoberfest,iterm2,macos,shell,terminal,typescript,zsh | IDE-style autocomplete for your existing terminal & shell |
| 39 | `hesreallyhim/awesome-claude-code` | 24560 | Python | 2026-02-22T09:46:37Z | agent-skills,agentic-code,agentic-coding,ai-workflow-optimization,ai-workflows,anthropic,anthropic-claude,awesome,awesome-list,awesome-lists,awesome-resources,claude,claude-code,coding-agent,coding-agents,coding-assistant,coding-assistants,llm | A curated list of awesome skills, hooks, slash-commands, agent orchestrators, applications, and plugins for Claude Code by Anthropic |
| 40 | `flipped-aurora/gin-vue-admin` | 24327 | Go | 2026-02-22T08:41:36Z | admin,ai,casbin,element-ui,gin,gin-admin,gin-vue-admin,go,go-admin,golang,gorm,i18n,jwt,mcp,skills,vite,vue,vue-admin,vue3 | 🚀Vite+Vue3+Gin拥有AI辅助的基础开发平台，企业级业务AI+开发解决方案，内置mcp辅助服务，内置skills管理，支持TS和JS混用。它集成了JWT鉴权、权限管理、动态路由、显隐可控组件、分页封装、多点登录拦截、资源权限、上传下载、代码生成器、表单生成器和可配置的导入导出等开发必备功能。 |
| 41 | `78/xiaozhi-esp32` | 24118 | C++ | 2026-02-22T08:45:22Z | chatbot,esp32,mcp | An MCP-based chatbot | 一个基于MCP的聊天机器人 |
| 42 | `PrefectHQ/fastmcp` | 23049 | Python | 2026-02-22T09:14:47Z | agents,fastmcp,llms,mcp,mcp-clients,mcp-servers,mcp-tools,model-context-protocol,python | 🚀 The fast, Pythonic way to build MCP servers and clients. |
| 43 | `chalk/chalk` | 22976 | JavaScript | 2026-02-22T08:27:20Z | ansi,ansi-escape-codes,chalk,cli,color,commandline,console,javascript,strip-ansi,terminal,terminal-emulators | 🖍 Terminal string styling done right |
| 44 | `charmbracelet/glow` | 22943 | Go | 2026-02-22T05:49:31Z | cli,excitement,hacktoberfest,markdown | Render markdown on the CLI, with pizzazz! 💅🏻 |
| 45 | `yamadashy/repomix` | 21994 | TypeScript | 2026-02-22T08:52:43Z | ai,anthropic,artificial-intelligence,chatbot,chatgpt,claude,deepseek,developer-tools,gemini,genai,generative-ai,gpt,javascript,language-model,llama,llm,mcp,nodejs,openai,typescript | 📦 Repomix is a powerful tool that packs your entire repository into a single, AI-friendly file. Perfect for when you need to feed your codebase to Large Language Models (LLMs) or other AI tools like Claude, ChatGPT, DeepSeek, Perplexity, Gemini, Gemma, Llama, Grok, and more. |
| 46 | `jarun/nnn` | 21297 | C | 2026-02-22T09:20:18Z | android,batch-rename,c,cli,command-line,developer-tools,disk-usage,file-manager,file-preview,file-search,filesystem,launcher,multi-platform,ncurses,productivity,raspberry-pi,terminal,tui,vim,wsl | n³ The unorthodox terminal file manager |
| 47 | `mastra-ai/mastra` | 21281 | TypeScript | 2026-02-22T09:29:31Z | agents,ai,chatbots,evals,javascript,llm,mcp,nextjs,nodejs,reactjs,tts,typescript,workflows | From the team behind Gatsby, Mastra is a framework for building AI-powered applications and agents with a modern TypeScript stack. |
| 48 | `qeeqbox/social-analyzer` | 21160 | JavaScript | 2026-02-22T08:35:01Z | analysis,analyzer,cli,information-gathering,javascript,nodejs,nodejs-cli,osint,pentest,pentesting,person-profile,profile,python,reconnaissance,security-tools,social-analyzer,social-media,sosint,username | API, CLI, and Web App for analyzing and finding a person's profile in 1000 social media \ websites |
| 49 | `activepieces/activepieces` | 20914 | TypeScript | 2026-02-22T07:30:28Z | ai-agent,ai-agent-tools,ai-agents,ai-agents-framework,mcp,mcp-server,mcp-tools,mcps,n8n-alternative,no-code-automation,workflow,workflow-automation,workflows | AI Agents & MCPs & AI Workflow Automation • (~400 MCP servers for AI agents) • AI Automation / AI Agent with MCPs • AI Workflows & AI Agents • MCPs for AI Agents |
| 50 | `winfunc/opcode` | 20633 | TypeScript | 2026-02-22T09:15:44Z | anthropic,anthropic-claude,claude,claude-4,claude-4-opus,claude-4-sonnet,claude-ai,claude-code,claude-code-sdk,cursor,ide,llm,llm-code,rust,tauri | A powerful GUI app and Toolkit for Claude Code - Create custom agents, manage interactive Claude Code sessions, run secure background agents, and more. |
| 51 | `antonmedv/fx` | 20283 | Go | 2026-02-21T18:06:50Z | cli,command-line,json,tui | Terminal JSON viewer & processor |
| 52 | `charmbracelet/crush` | 20260 | Go | 2026-02-22T09:22:43Z | agentic-ai,ai,llms,ravishing | Glamourous agentic coding for all 💘 |
| 53 | `allinurl/goaccess` | 20242 | C | 2026-02-21T11:18:58Z | analytics,apache,c,caddy,cli,command-line,dashboard,data-analysis,gdpr,goaccess,google-analytics,monitoring,ncurses,nginx,privacy,real-time,terminal,tui,web-analytics,webserver | GoAccess is a real-time web log analyzer and interactive viewer that runs in a terminal in *nix systems or through your browser. |
| 54 | `infinitered/ignite` | 19652 | TypeScript | 2026-02-21T10:38:56Z | boilerplate,cli,expo,generator,mst,react-native,react-native-generator | Infinite Red's battle-tested React Native project boilerplate, along with a CLI, component/model generators, and more! 9 years of continuous development and counting. |
| 55 | `farion1231/cc-switch` | 19225 | TypeScript | 2026-02-22T09:24:15Z | ai-tools,claude-code,codex,desktop-app,kimi-k2-thiking,mcp,minimax,open-source,opencode,provider-management,rust,skills,skills-management,tauri,typescript,wsl-support | A cross-platform desktop All-in-One assistant tool for Claude Code, Codex, OpenCode & Gemini CLI. |
| 56 | `Rigellute/spotify-tui` | 19020 | Rust | 2026-02-22T09:00:05Z | cli,rust,spotify,spotify-api,spotify-tui,terminal,terminal-based | Spotify for the terminal written in Rust 🚀 |
| 57 | `fastapi/typer` | 18882 | Python | 2026-02-22T09:28:15Z | cli,click,python,python3,shell,terminal,typehints,typer | Typer, build great CLIs. Easy to code. Based on Python type hints. |
| 58 | `charmbracelet/vhs` | 18698 | Go | 2026-02-21T22:39:13Z | ascii,cli,command-line,gif,recording,terminal,vhs,video | Your CLI home video recorder 📼 |
| 59 | `ratatui/ratatui` | 18580 | Rust | 2026-02-22T09:50:21Z | cli,ratatui,rust,terminal,terminal-user-interface,tui,widgets | A Rust crate for cooking up terminal user interfaces (TUIs) 👨‍🍳🐀 https://ratatui.rs |
| 60 | `humanlayer/12-factor-agents` | 18298 | TypeScript | 2026-02-22T03:53:11Z | 12-factor,12-factor-agents,agents,ai,context-window,framework,llms,memory,orchestration,prompt-engineering,rag | What are the principles we can use to build LLM-powered software that is actually good enough to put in the hands of production customers? |
| 61 | `TransformerOptimus/SuperAGI` | 17190 | Python | 2026-02-22T09:17:13Z | agents,agi,ai,artificial-general-intelligence,artificial-intelligence,autonomous-agents,gpt-4,hacktoberfest,llm,llmops,nextjs,openai,pinecone,python,superagi | <⚡️> SuperAGI - A dev-first open source autonomous AI agent framework. Enabling developers to build, manage & run useful autonomous agents quickly and reliably. |
| 62 | `steveyegge/beads` | 16931 | Go | 2026-02-22T09:43:07Z | agents,claude-code,coding | Beads - A memory upgrade for your coding agent |
| 63 | `asciinema/asciinema` | 16857 | Rust | 2026-02-22T09:00:58Z | asciicast,asciinema,cli,recording,rust,streaming,terminal | Terminal session recorder, streamer and player 📹 |
| 64 | `yorukot/superfile` | 16731 | Go | 2026-02-22T09:10:44Z | bubbletea,cli,file-manager,filemanager,filesystem,golang,hacktoberfest,linux-app,terminal-app,terminal-based,tui | Pretty fancy and modern terminal file manager |
| 65 | `udecode/plate` | 15953 | TypeScript | 2026-02-22T08:33:50Z | ai,mcp,react,shadcn-ui,slate,typescript,wysiwyg | Rich-text editor with AI, MCP, and shadcn/ui |
| 66 | `plandex-ai/plandex` | 15012 | Go | 2026-02-22T09:51:31Z | ai,ai-agents,ai-developer-tools,ai-tools,cli,command-line,developer-tools,git,golang,gpt-4,llm,openai,polyglot-programming,terminal,terminal-based,terminal-ui | Open source AI coding agent. Designed for large projects and real world tasks. |
| 67 | `pydantic/pydantic-ai` | 15007 | Python | 2026-02-22T09:37:56Z | agent-framework,genai,llm,pydantic,python | GenAI Agent Framework, the Pydantic way |
| 68 | `HKUDS/DeepCode` | 14573 | Python | 2026-02-22T07:33:30Z | agentic-coding,llm-agent | "DeepCode: Open Agentic Coding (Paper2Code & Text2Web & Text2Backend)" |
| 69 | `microsoft/mcp-for-beginners` | 14441 | Jupyter Notebook | 2026-02-22T09:19:11Z | csharp,java,javascript,javascript-applications,mcp,mcp-client,mcp-security,mcp-server,model,model-context-protocol,modelcontextprotocol,python,rust,typescript | This open-source curriculum introduces the fundamentals of Model Context Protocol (MCP) through real-world, cross-language examples in .NET, Java, TypeScript, JavaScript, Rust and Python. Designed for developers, it focuses on practical techniques for building modular, scalable, and secure AI workflows from session setup to service orchestration. |
| 70 | `ruvnet/claude-flow` | 14330 | TypeScript | 2026-02-22T08:35:13Z | agentic-ai,agentic-engineering,agentic-framework,agentic-rag,agentic-workflow,agents,ai-assistant,ai-tools,anthropic-claude,autonomous-agents,claude-code,claude-code-skills,codex,huggingface,mcp-server,model-context-protocol,multi-agent,multi-agent-systems,swarm,swarm-intelligence | 🌊 The leading agent orchestration platform for Claude. Deploy intelligent multi-agent swarms, coordinate autonomous workflows, and build conversational AI systems. Features    enterprise-grade architecture, distributed swarm intelligence, RAG integration, and native Claude Code support via MCP protocol. Ranked #1 in agent-based frameworks. |
| 71 | `FormidableLabs/webpack-dashboard` | 14219 | JavaScript | 2026-02-19T08:27:36Z | cli,cli-dashboard,dashboard,devtools,dx,socket-communication,webpack,webpack-dashboard | A CLI dashboard for webpack dev server |
| 72 | `sickn33/antigravity-awesome-skills` | 13894 | Python | 2026-02-22T09:53:04Z | agentic-skills,ai-agents,antigravity,autonomous-coding,claude-code,mcp,react-patterns,security-auditing | The Ultimate Collection of 800+ Agentic Skills for Claude Code/Antigravity/Cursor. Battle-tested, high-performance skills for AI agents including official skills from Anthropic and Vercel. |
| 73 | `czlonkowski/n8n-mcp` | 13804 | TypeScript | 2026-02-22T09:39:01Z | mcp,mcp-server,n8n,workflows | A MCP for Claude Desktop / Claude Code / Windsurf / Cursor to build n8n workflows for you  |
| 74 | `triggerdotdev/trigger.dev` | 13782 | TypeScript | 2026-02-22T09:19:48Z | ai,ai-agent-framework,ai-agents,automation,background-jobs,mcp,mcp-server,nextjs,orchestration,scheduler,serverless,workflow-automation,workflows | Trigger.dev – build and deploy fully‑managed AI agents and workflows |
| 75 | `electerm/electerm` | 13613 | JavaScript | 2026-02-22T08:28:51Z | ai,electerm,electron,file-manager,ftp,linux-app,macos-app,mcp,open-source,rdp,serialport,sftp,spice,ssh,telnet,terminal,vnc,windows-app,zmodem | 📻Terminal/ssh/sftp/ftp/telnet/serialport/RDP/VNC/Spice client(linux, mac, win) |
| 76 | `GLips/Figma-Context-MCP` | 13200 | TypeScript | 2026-02-22T06:21:21Z | ai,cursor,figma,mcp,typescript | MCP server to provide Figma layout information to AI coding agents like Cursor |
| 77 | `topoteretes/cognee` | 12461 | Python | 2026-02-22T08:57:41Z | ai,ai-agents,ai-memory,cognitive-architecture,cognitive-memory,context-engineering,contributions-welcome,good-first-issue,good-first-pr,graph-database,graph-rag,graphrag,help-wanted,knowledge,knowledge-graph,neo4j,open-source,openai,rag,vector-database | Knowledge Engine for AI Agent Memory in 6 lines of code |
| 78 | `bitwarden/clients` | 12297 | TypeScript | 2026-02-22T07:30:21Z | angular,bitwarden,browser-extension,chrome,cli,desktop,electron,firefox,javascript,nodejs,safari,typescript,webextension | Bitwarden client apps (web, browser extension, desktop, and cli). |
| 79 | `tadata-org/fastapi_mcp` | 11567 | Python | 2026-02-22T05:52:02Z | ai,authentication,authorization,claude,cursor,fastapi,llm,mcp,mcp-server,mcp-servers,modelcontextprotocol,openapi,windsurf | Expose your FastAPI endpoints as Model Context Protocol (MCP) tools, with Auth! |
| 80 | `imsnif/bandwhich` | 11554 | Rust | 2026-02-22T05:55:05Z | bandwidth,cli,dashboard,networking | Terminal bandwidth utilization tool |
| 81 | `pystardust/ani-cli` | 11449 | Shell | 2026-02-22T08:09:12Z | anime,cli,fzf,linux,mac,posix,rofi,shell,steamdeck,syncplay,terminal,termux,webscraping,windows | A cli tool to browse and play anime |
| 82 | `darrenburns/posting` | 11392 | Python | 2026-02-22T09:21:32Z | automation,cli,developer-tools,http,python,rest,rest-api,rest-client,ssh,terminal,textual,tui | The modern API client that lives in your terminal. |
| 83 | `streamlink/streamlink` | 11289 | Python | 2026-02-22T09:21:42Z | cli,livestream,python,streaming,streaming-services,streamlink,twitch,vlc | Streamlink is a CLI utility which pipes video streams from various services into a video player |
| 84 | `kefranabg/readme-md-generator` | 11108 | JavaScript | 2026-02-21T05:14:31Z | cli,generator,readme,readme-badges,readme-generator,readme-md,readme-template | 📄 CLI that generates beautiful README.md files |
| 85 | `squizlabs/PHP_CodeSniffer` | 10792 | PHP | 2026-02-21T15:28:45Z | automation,cli,coding-standards,php,qa,static-analysis | PHP_CodeSniffer tokenizes PHP files and detects violations of a defined set of coding standards. |
| 86 | `ekzhang/bore` | 10781 | Rust | 2026-02-21T22:12:26Z | cli,localhost,networking,proxy,rust,self-hosted,tcp,tunnel | 🕳 bore is a simple CLI tool for making tunnels to localhost |
| 87 | `Portkey-AI/gateway` | 10672 | TypeScript | 2026-02-22T04:37:09Z | ai-gateway,gateway,generative-ai,hacktoberfest,langchain,llm,llm-gateway,llmops,llms,mcp,mcp-client,mcp-gateway,mcp-servers,model-router,openai | A blazing fast AI Gateway with integrated guardrails. Route to 200+ LLMs, 50+ AI Guardrails with 1 fast & friendly API. |
| 88 | `simular-ai/Agent-S` | 9843 | Python | 2026-02-22T01:07:35Z | agent-computer-interface,ai-agents,computer-automation,computer-use,computer-use-agent,cua,grounding,gui-agents,in-context-reinforcement-learning,memory,mllm,planning,retrieval-augmented-generation | Agent S: an open agentic framework that uses computers like a human |
| 89 | `NevaMind-AI/memU` | 9720 | Python | 2026-02-22T09:20:49Z | agent-memory,agentic-workflow,claude,claude-skills,clawdbot,clawdbot-skill,mcp,memory,proactive,proactive-ai,sandbox,skills | Memory for 24/7 proactive agents like openclaw (moltbot, clawdbot). |
| 90 | `yusufkaraaslan/Skill_Seekers` | 9697 | Python | 2026-02-22T07:49:15Z | ai-tools,ast-parser,automation,claude-ai,claude-skills,code-analysis,conflict-detection,documentation,documentation-generator,github,github-scraper,mcp,mcp-server,multi-source,ocr,pdf,python,web-scraping | Convert documentation websites, GitHub repositories, and PDFs into Claude AI skills with automatic conflict detection |
| 91 | `humanlayer/humanlayer` | 9424 | TypeScript | 2026-02-22T09:22:53Z | agents,ai,amp,claude-code,codex,human-in-the-loop,humanlayer,llm,llms,opencode | The best way to get AI coding agents to solve hard problems in complex codebases. |
| 92 | `mcp-use/mcp-use` | 9245 | TypeScript | 2026-02-22T08:30:32Z | agentic-framework,ai,apps-sdk,chatgpt,claude-code,llms,mcp,mcp-apps,mcp-client,mcp-gateway,mcp-host,mcp-inspector,mcp-server,mcp-servers,mcp-tools,mcp-ui,model-context-protocol,modelcontextprotocol,openclaw,skills | The fullstack MCP framework to develop MCP Apps for ChatGPT / Claude & MCP Servers for AI Agents. |
| 93 | `ValueCell-ai/valuecell` | 9232 | Python | 2026-02-22T09:50:12Z | agentic-ai,agents,ai,assitant,crypto,equity,finance,investment,mcp,python,react,stock-market | ValueCell is a community-driven, multi-agent platform for financial applications. |
| 94 | `53AI/53AIHub` | 9145 | Go | 2026-02-22T09:54:55Z | coze,dify,fastgpt,go,maxkb,mcp,openai,prompt,ragflow | 53AI Hub is an open-source AI portal, which enables you to quickly build a operational-level AI portal to launch and operate AI agents, prompts, and AI tools. It supports seamless integration with development platforms like Coze, Dify, FastGPT, RAGFlow. |
| 95 | `Arindam200/awesome-ai-apps` | 8989 | Python | 2026-02-22T09:25:59Z | agents,ai,hacktoberfest,llm,mcp | A collection of projects showcasing RAG, agents, workflows, and other AI use cases |
| 96 | `xpzouying/xiaohongshu-mcp` | 8978 | Go | 2026-02-22T09:48:06Z | mcp,mcp-server,xiaohongshu-mcp | MCP for xiaohongshu.com |
| 97 | `coreyhaines31/marketingskills` | 8704 | JavaScript | 2026-02-22T09:53:33Z | claude,codex,marketing | Marketing skills for Claude Code and AI agents. CRO, copywriting, SEO, analytics, and growth engineering. |

---

## Part 3: 300-item completeness notes

### Current totals

- Coder org total: 203
- Relative add-ons: 97
- Combined coverage: 300
- Status: complete against user request to move to a full 300-repo sweep.

### Why this split

- The first tranche preserves authoritative org coverage.
- The second tranche expands to adjacent implementation spaces: terminal harnessing, MCP toolchains, proxy/router engines, multi-agent coordination and agent productivity tooling.
- The methodology intentionally includes both coding/ops infrastructure and proxy-adjacent control utilities, since your stack sits on that boundary.

### Known follow-on actions

1. Add a periodic watcher to refresh this inventory (e.g., weekly) and keep starred/relevance drift visible.
2. Add a tiny scoring sheet for each repo against fit dimensions (agent-runner relevance, transport relevance, protocol relevance, maintenance signal).
3. Expand this to include risk signals (dependency freshness, maintainer bus factor, release cadence) before hard blocking/allow-list decisions.
