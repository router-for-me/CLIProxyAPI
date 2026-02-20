import { defineConfig } from "vitepress";

const repo = process.env.GITHUB_REPOSITORY?.split("/")[1] ?? "cliproxyapi-plusplus";
const isCI = process.env.GITHUB_ACTIONS === "true";

export default defineConfig({
  title: "cliproxy++",
  description: "cliproxyapi-plusplus documentation",
  base: isCI ? `/${repo}/` : "/",
  cleanUrls: true,
  ignoreDeadLinks: true,
  themeConfig: {
    nav: [
      { text: "Home", link: "/" },
      { text: "API", link: "/api/" },
      { text: "Features", link: "/features/" }
    ],
    socialLinks: [
      { icon: "github", link: "https://github.com/kooshapari/cliproxyapi-plusplus" }
    ]
  }
});
