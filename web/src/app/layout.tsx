import type { Metadata } from "next";
import { DM_Sans, Noto_Sans } from "next/font/google";
import "./globals.css";
import { ClientProviders } from "@/components/client-providers";
import { cn } from "@/lib/utils";

const notoSansHeading = Noto_Sans({
  subsets: ["latin"],
  variable: "--font-heading",
});

const dmSans = DM_Sans({
  subsets: ["latin"],
  variable: "--font-sans",
});

export const metadata: Metadata = {
  title: "CLI Proxy API Management",
  description:
    "Management dashboard for CLI Proxy API server providing OpenAI/Gemini/Claude compatible APIs",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={cn("h-full", "antialiased", dmSans.variable, notoSansHeading.variable)}
      suppressHydrationWarning
    >
      <body className="min-h-full flex flex-col">
        <ClientProviders>{children}</ClientProviders>
      </body>
    </html>
  );
}
