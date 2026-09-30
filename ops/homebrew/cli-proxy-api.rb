class CliProxyApi < Formula
  desc "Proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs"
  homepage "https://github.com/hrygo/CLIProxyAPI"
  version "1.0.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_aarch64.tar.gz"
      sha256 "fc1d28a2f4362c14413a0b3d709809c1f1f3aceb935a15d65b64963a97ab28c5"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_amd64.tar.gz"
      sha256 "627b04f2308913189c996db5842159106880b8a2e977a1eb1638c9acaf0c87b1"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_aarch64.tar.gz"
      sha256 "cdbb8864aed04a593ef901723b0c497b85bfee46e49c2e792920f17bc18996fa"
    else
      url "https://github.com/hrygo/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_amd64.tar.gz"
      sha256 "82f7626ab0f3a6a5f02c3dea7bdc8e64af0f49888b00197aa4b0fbdb73b93688"
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
