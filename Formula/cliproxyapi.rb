class Cliproxyapi < Formula
  desc "CLI API proxy service for OpenAI, Gemini, Claude, and Codex-compatible clients"
  homepage "https://github.com/router-for-me/CLIProxyAPI"
  version "6.8.49"
  license "MIT"

  livecheck do
    url :stable
    strategy :github_latest
  end

  on_macos do
    on_arm do
      url "https://github.com/router-for-me/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_arm64.tar.gz"
      sha256 "5dfa1e359d8e44601b3cc0aa6f665d4a376ec568187d24a62155dcae6515bdb3"
    end

    on_intel do
      url "https://github.com/router-for-me/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_darwin_amd64.tar.gz"
      sha256 "ce555eb14b43eff3b3c06d53f592ac1cfa2edfe200f588f4b823c4083bbc22d8"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/router-for-me/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_arm64.tar.gz"
      sha256 "52fef4a1a9822a08d2c47e499531be06c6736b13347be3b4eb06fe5b0a3a77da"
    end

    on_intel do
      url "https://github.com/router-for-me/CLIProxyAPI/releases/download/v#{version}/CLIProxyAPI_#{version}_linux_amd64.tar.gz"
      sha256 "b4c547910faa7331bc55e268e759f4bb0f2a0af8f0d09b8a356153404c16aff9"
    end
  end

  def install
    bin.install "cli-proxy-api"
    pkgshare.install "config.example.yaml" if File.exist?("config.example.yaml")
  end

  test do
    output = shell_output("#{bin}/cli-proxy-api --help 2>&1")
    assert_match "usage", output.downcase
  end
end
