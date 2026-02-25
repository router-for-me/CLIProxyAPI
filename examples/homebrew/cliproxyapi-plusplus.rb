class CliproxyapiPlusplus < Formula
  desc "LLM proxy CLI for OpenAI-compatible and provider-specific APIs"
  homepage "https://github.com/router-for-me/CLIProxyAPI"
  head "https://github.com/router-for-me/CLIProxyAPI.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", "-o", bin/"cliproxyapi++", "./cmd/server"
  end

  service do
    run [opt_bin/"cliproxyapi++", "--config", etc/"cliproxyapi/config.yaml"]
    working_dir var/"log/cliproxyapi"
    keep_alive true
    log_path var/"log/cliproxyapi-plusplus.log"
    error_log_path var/"log/cliproxyapi-plusplus.err"
  end

  def post_install
    (etc/"cliproxyapi").mkpath
  end

  test do
    assert_predicate bin/"cliproxyapi++", :exist?
  end
end
