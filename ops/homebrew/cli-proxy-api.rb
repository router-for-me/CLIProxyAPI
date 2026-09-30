class CliProxyApi < Formula
  desc "Proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs"
  homepage "https://github.com/hrygo/CLIProxyAPI"
  version "1.0.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_aarch64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_DARWIN_ARM64"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_DARWIN_AMD64"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_aarch64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_ARM64"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_LINUX_AMD64"
    end
  end

  def install
    bin.install "cli-proxy-api"
    pkgshare.install "config.example.yaml"
  end

  def caveats
    <<~EOS
      Configuration is not managed by Homebrew. Pass your own config path:
        cliproxyapi -config /opt/homebrew/etc/cliproxyapi.conf
    EOS
  end
end
