# ctxview — 上下文窗口检视器 (Context Window Viewer)

单文件本地 Web 检视器与反向代理抓包网关，用于拆解分析 LLM 编码代理的上下文窗口构成、提示词缓存命中率与历史轮次增长情况。

## 核心能力

- **本地会话自动扫描**：开箱扫描 `~/.claude/projects`（Claude Code）、`~/.codex/sessions`（Codex）与 `~/.joycode/sessions`（JoyCode），并支持在界面中自定义扫描目录。
- **HTTP 反向代理抓包**：暴露 `/v1/messages`、`/v1/responses`、`/v1/chat/completions`、`/v1beta/`，可实时拦截并持久化流式/非流式请求的真实 Prompt 与 Usage。
- **默认隐私脱敏**：扫描与展示默认启用内置规则脱敏（密钥、Token、邮箱、电话、主目录路径等），支持界面一键切换开启/关闭。
- **零外部依赖单二进制**：全部前端页面、CSS/JS、图标与词库已通过 `//go:embed` 内置到可执行文件中。

---

## 安装

- **方式 A — Homebrew（macOS / Linux）**
  ```bash
  brew install Iapa/tap/ctxview

  # 可选：注册为后台服务
  brew services start ctxview
  ```
- **方式 B — 一键安装脚本（macOS / Linux）**
  ```bash
  curl -fsSL https://raw.githubusercontent.com/Iapa/ctxview/main/install.sh | sh
  ```
- **方式 C — Go 源码安装**
  ```bash
  go install github.com/Iapa/ctxview@latest
  ```

---

## 快速启动与自定义端口

```bash
# 默认端口 8787 并自动打开浏览器
ctxview -open

# 自定义端口 / 监听地址（支持参数与环境变量）
ctxview -p 9000 -open
ctxview -host 127.0.0.1 -port 9000
CTXVIEW_PORT=9000 ctxview
```

常用参数与环境变量：

- `-p` / `-port 8787`：自定义监听端口（优先级高于环境变量，也可用 `CTXVIEW_PORT` 或 `PORT`）
- `-host`：监听主机/IP（例如 `127.0.0.1` 或 `0.0.0.0`，也可用 `CTXVIEW_HOST`）
- `-addr :8787`：完整 HTTP 监听地址（支持 `:9000`、`127.0.0.1:9000` 或纯端口号 `9000`，也可用 `CTXVIEW_ADDR`）
- `-data-dir ~/.ctxview/data`：代理抓包与自定义配置存储目录（若当前目录已有 `.ctxview-data` 则优先复用）
- `-session-dir /path/a,/path/b`：额外扫描的会话目录或 `.jsonl` 文件
- `-open`：启动后自动打开系统浏览器
- `-no-demo`：禁用内置示例会话
- `-version`：输出当前版本

---

## 本地构建与测试

```bash
make test
make build
make dist
```
