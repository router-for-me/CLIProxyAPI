import { defineConfig } from "vitepress";
import { contentTabsPlugin } from "./plugins/content-tabs";

const repo = process.env.GITHUB_REPOSITORY?.split("/")[1] ?? "cliproxyapi-plusplus";
const isCI = process.env.GITHUB_ACTIONS === "true";
const docsBase = isCI ? `/${repo}/` : "/";
const faviconHref = `${docsBase}favicon.ico`;

<<<<<<< HEAD
// Supported locales: en, zh-CN, zh-TW, fa, fa-Latn
const locales = {
  root: {
    label: "English",
    lang: "en",
    title: "cliproxy++",
    description: "cliproxyapi-plusplus documentation"
  },
  "zh-CN": {
    label: "简体中文",
    lang: "zh-CN",
    title: "cliproxy++",
    description: "cliproxyapi-plusplus 文档"
  },
  "zh-TW": {
    label: "繁體中文",
    lang: "zh-TW",
    title: "cliproxy++",
    description: "cliproxyapi-plusplus 文檔"
  },
  fa: {
    label: "فارسی",
    lang: "fa",
    title: "cliproxy++",
    description: "مستندات cliproxyapi-plusplus"
  },
  "fa-Latn": {
    label: "Pinglish",
    lang: "fa-Latn",
    title: "cliproxy++",
    description: "cliproxyapi-plusplus docs (Latin)"
  }
};

=======
>>>>>>> archive/pr-234-head-20260223
export default defineConfig({
  title: "cliproxy++",
  description: "cliproxyapi-plusplus documentation",
  base: docsBase,
<<<<<<< HEAD
  locales,
=======
>>>>>>> archive/pr-234-head-20260223
  head: [
    ["link", { rel: "icon", href: faviconHref }]
  ],
  cleanUrls: true,
  ignoreDeadLinks: [/^http:\/\/localhost:\d+(\/.*)?$/],
  lastUpdated: true,
  themeConfig: {
    nav: [
      { text: "Home", link: "/" },
<<<<<<< HEAD
      { text: "Getting Started", link: "/getting-started" },
      { text: "Providers", link: "/provider-usage" },
      { text: "Provider Catalog", link: "/provider-catalog" },
      { text: "Planning", link: "/planning/" },
      { text: "Reference", link: "/routing-reference" },
      { text: "API", link: "/api/" },
      { text: "Docsets", link: "/docsets/" },
      {
        text: "🌐 Language",
        items: [
          { text: "English", link: "/" },
          { text: "简体中文", link: "/zh-CN/" },
          { text: "繁體中文", link: "/zh-TW/" },
          { text: "فارسی", link: "/fa/" },
          { text: "Pinglish", link: "/fa-Latn/" }
        ]
      }
=======
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
>>>>>>> archive/pr-234-head-20260223
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
  },

  markdown: {
    config: (md) => {
      md.use(contentTabsPlugin)
    }
  }
});
