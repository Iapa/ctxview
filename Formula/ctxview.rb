class Ctxview < Formula
  desc "Local Context Window Inspector and reverse-proxy analyzer for LLM coding agents"
  homepage "https://github.com/Iapa/ctxview"
  version "0.1.0"
  license "MIT"
  head "https://github.com/Iapa/ctxview.git", branch: "main"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Iapa/ctxview/releases/download/v#{version}/ctxview_#{version}_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    else
      url "https://github.com/Iapa/ctxview/releases/download/v#{version}/ctxview_#{version}_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Iapa/ctxview/releases/download/v#{version}/ctxview_#{version}_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_ARM64_SHA256"
    else
      url "https://github.com/Iapa/ctxview/releases/download/v#{version}/ctxview_#{version}_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "."
    else
      bin.install "ctxview"
    end
  end

  service do
    run [opt_bin/"ctxview"]
    environment_variables CTXVIEW_PORT: "8787"
    keep_alive true
    log_path var/"log/ctxview.log"
    error_log_path var/"log/ctxview.log"
  end

  test do
    assert_match(/\S+/, shell_output("#{bin}/ctxview -version").strip)
  end
end

