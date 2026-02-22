import { defineConfig } from "vitepress";

const repo = process.env.GITHUB_REPOSITORY?.split("/")[1] ?? "cliproxyapi-plusplus";
const isCI = process.env.GITHUB_ACTIONS === "true";

export default defineConfig({
  title: "cliproxy++",
  description: "cliproxyapi-plusplus documentation",
  base: isCI ? `/${repo}/` : "/",
  cleanUrls: true,
  ignoreDeadLinks: [/^http:\/\/localhost:\d+(\/.*)?$/],
  lastUpdated: true,
  themeConfig: {
    nav: [
      { text: "Home", link: "/" },
      { text: "Getting Started", link: "/getting-started" },
      { text: "Providers", link: "/provider-usage" },
      { text: "Provider Catalog", link: "/provider-catalog" },
      { text: "Planning", link: "/planning/" },
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
    search: {
      provider: "local"
    },
    footer: {
      message: "MIT Licensed",
      copyright: "Copyright © KooshaPari"
    },
    editLink: {
      pattern:
        "https://github.com/kooshapari/cliproxyapi-plusplus/edit/main/docs/:path",
      text: "Edit this page on GitHub"
    },
    outline: {
      level: [2, 3]
    },
    socialLinks: [
      { icon: "github", link: "https://github.com/kooshapari/cliproxyapi-plusplus" }
    ]
  }
});
