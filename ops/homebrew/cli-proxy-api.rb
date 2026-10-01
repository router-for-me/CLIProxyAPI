# Template for the hrygo/homebrew-cliproxyapi tap. Never publish this file
# as-is: ops/homebrew/render-formula.py substitutes the release tag, the asset
# version and the four checksums, and fails when the rendered version and the
# asset filenames disagree. The placeholders below are deliberately not valid
# release references, so an unrendered copy can never look installable.
class CliProxyApi < Formula
  desc "Proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs"
  homepage "https://github.com/hrygo/CLIProxyAPI"
  # Declared explicitly rather than derived from the URL. Release tags carry an
  # -upstreamX.Y.Z suffix, and Homebrew's URL-derived version parsing is not
  # reliable for that shape.
  version "0.0.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/vX.Y.Z/CLIProxyAPI_X.Y.Z_darwin_aarch64.tar.gz"
      sha256 "fc1d28a2f4362c14413a0b3d709809c1f1f3aceb935a15d65b64963a97ab28c5"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/vX.Y.Z/CLIProxyAPI_X.Y.Z_darwin_amd64.tar.gz"
      sha256 "627b04f2308913189c996db5842159106880b8a2e977a1eb1638c9acaf0c87b1"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/vX.Y.Z/CLIProxyAPI_X.Y.Z_linux_aarch64.tar.gz"
      sha256 "cdbb8864aed04a593ef901723b0c497b85bfee46e49c2e792920f17bc18996fa"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/vX.Y.Z/CLIProxyAPI_X.Y.Z_linux_amd64.tar.gz"
      sha256 "82f7626ab0f3a6a5f02c3dea7bdc8e64af0f49888b00197aa4b0fbdb73b93688"
    end
  end

  def install
    # The upstream release archive names the binary cli-proxy-api. Install it
    # as cliproxyapi so the existing launchd agent path keeps working.
    bin.install "cli-proxy-api" => "cliproxyapi"
    pkgshare.install "config.example.yaml"
  end

  def caveats
    <<~EOS
      Configuration is not managed by Homebrew. Pass your own config path:
        cliproxyapi -config #{etc}/cliproxyapi.conf

      Upgrading changes the installed binary. Restart your service manager
      separately to load it. For the existing macOS LaunchAgent:
        launchctl kickstart -k gui/$(id -u)/com.hrygo.cliproxyapi
    EOS
  end

  test do
    output = shell_output("#{bin}/cliproxyapi -h 2>&1")
    assert_match "CLIProxyAPI Version:", output
    assert_match version.to_s, output
  end
end
