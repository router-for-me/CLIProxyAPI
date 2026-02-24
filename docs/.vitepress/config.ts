import { defineConfig } from "vitepress";
import { contentTabsPlugin } from "./plugins/content-tabs";

// Import shared base config
import baseConfig from "../../../docs-hub/.vitepress/base.config";

const repo = process.env.GITHUB_REPOSITORY?.split("/")[1] ?? "cliproxyapi-plusplus";
const isCI = process.env.GITHUB_ACTIONS === "true";
const docsBase = isCI ? `/${repo}/` : "/";

export default defineConfig({
  ...baseConfig,
  // Ensure dead links are ignored (for localhost links in docs)
  ignoreDeadLinks: true,
  cleanUrls: true,
  // Project-specific overrides
  title: "cliproxy++",
  description: "cliproxyapi-plusplus documentation",
  base: docsBase,
  head: [
    ["link", { rel: "icon", href: `${docsBase}favicon.ico` }]
  ],
  themeConfig: {
    ...baseConfig.themeConfig,
    nav: [
      { text: "Home", link: "/" },
      { text: "Start Here", link: "/start-here" },
      { text: "Tutorials", link: "/tutorials/" },
      { text: "How-to", link: "/how-to/" },
      { text: "Explanation", link: "/explanation/" },
      { text: "Getting Started", link: "/getting-started" },
      { text: "Providers", link: "/provider-usage" },
      { text: "Provider Catalog", link: "/provider-catalog" },
      { text: "Operations", link: "/operations/" },
      { text: "Reference", link: "/routing-reference" },
      { text: "API", link: "/api/" },
      { text: "Docsets", link: "/docsets/" }
    ],
    sidebar: [
      {
        text: "Guide",
        items: [
          { text: "Overview", link: "/" },
          { text: "Getting Started", link: "/getting-started" },
          { text: "Install", link: "/install" },
          { text: "Provider Usage", link: "/provider-usage" },
          { text: "Provider Catalog", link: "/provider-catalog" },
          { text: "Provider Operations", link: "/provider-operations" },
          { text: "Troubleshooting", link: "/troubleshooting" },
          { text: "Planning Boards", link: "/planning/" }
        ]
      },
      {
        text: "Reference",
        items: [
          { text: "Routing and Models", link: "/routing-reference" },
          { text: "Feature Guides", link: "/features/" },
          { text: "Docsets", link: "/docsets/" }
        ]
      },
      {
        text: "API",
        items: [
          { text: "API Index", link: "/api/" },
          { text: "OpenAI-Compatible API", link: "/api/openai-compatible" },
          { text: "Management API", link: "/api/management" },
          { text: "Operations API", link: "/api/operations" }
        ]
      }
    ],
    footer: {
      message: "MIT Licensed",
      copyright: "Copyright © KooshaPari"
    },
    editLink: {
      pattern:
        "https://github.com/kooshapari/cliproxyapi-plusplus/edit/main/docs/:path",
      text: "Edit this page on GitHub"
    },
    socialLinks: [
      { icon: "github", link: "https://github.com/kooshapari/cliproxyapi-plusplus" }
    ]
  },

  markdown: {
    config: (md) => {
      md.use(contentTabsPlugin)
    }
  }
});
