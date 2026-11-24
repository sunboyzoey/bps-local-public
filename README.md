# bps-local

**QQ 交流群：`1135421637`** · 项目使用与问题交流。

将本机的 Responses API 请求转换为 ChatGPT Excel / Basispoints（BPS）请求的 Go 命令行桥接器。客户端通过 HTTP 发送请求、通过 SSE 接收事件；桥接器通过 WebSocket 连接 BPS 上游。

```text
Responses 客户端 → http://127.0.0.1:8790/v1/responses → BPS WebSocket
```

本仓库只发布独立 CLI，不包含 CPA 插件或管理服务。BPS 属于产品内部接口，本项目并非 OpenAI 官方项目；接口、授权方式和模型权限可能变化，代码可构建不代表目前的上游请求一定成功。

## 快速开始

安装 Go 1.26.0 或更高版本，进入本目录构建：

```bash
go build -o bps-local ./cmd/bps-local
```

以下示例使用本机 `7890` 代理端口，请替换成自己的代理地址。先完成独立 BPS 授权，默认使用设备码登录：

```bash
./bps-local --login --proxy http://127.0.0.1:7890
```

终端显示设备码并打开授权页面，由你完成登录与确认。默认保存到 `~/.bps-local/oauth.json`；若文件已存在，程序拒绝覆盖，可用 `--auth` 指定另一个文件。程序不会自动读取或修改 Codex 的授权文件。

也可以使用普通浏览器授权，无需输入设备码。需要已安装 Chrome、Chromium 或 Edge：

```bash
./bps-local --login --login-method browser \
  --auth "$HOME/.bps-local/browser-oauth.json" \
  --proxy http://127.0.0.1:7890
```

程序打开独立的临时浏览器窗口，由你完成登录、验证码和授权；成功后自动保存凭据。它不读取已有 Chrome、Excel 或 Codex 的登录信息，结束时关闭本次窗口并清理临时 profile。无法自动找到浏览器时，用 `--browser-path` 指定其可执行文件路径。浏览器模式仅支持无用户名密码的代理；带认证的上游代理需先通过本地转发使用。

无桌面的 Linux 使用设备码模式加 `--no-browser`，在其他电脑打开终端输出的网址完成授权。`--login-method browser` 不支持 `--no-browser`。详细步骤见 [独立授权](docs/USAGE.md#2-独立授权)。

启动桥接器，保持该终端运行：

```bash
./bps-local --proxy http://127.0.0.1:7890
```

若登录时指定了另一个 OAuth 文件，启动时也需指定同一个 `--auth` 路径。

在另一终端调用本地接口。将 `YOUR_MODEL` 换成该账号实际有权使用的模型：

```bash
curl -N http://127.0.0.1:8790/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"YOUR_MODEL","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}'
```

本地接口仅监听回环地址，不要求客户端提供 API Key。它返回流式事件；应检查最终事件和内容，HTTP 连接成功不等于生成成功。

## 6.2 模型

CLI `0.4.0` 为 `gpt-6.2-astra` 和 `gpt-6.2-sol` 增加独立的推理强度处理。模型 ID 原样发送，不设置别名，也不会在上游失败后换成其他模型。例如：

```bash
curl -N http://127.0.0.1:8790/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6.2-sol","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}'
```

使用 `gpt-6.2-astra` 时替换示例中的模型 ID 即可。这两个模型的 `low`、`medium`、`high`、`xhigh`、`max` 原样发送，省略强度时使用 `medium`；`ultra` 会被拒绝，需要最高档时请显式填写 `max`。兼容值映射及其他模型行为见 [请求与会话](docs/USAGE.md#4-请求与会话)。

本次验证覆盖本地转换与 mock WebSocket 转发，未执行真实上游验收。账号能否使用这两个模型、BPS 是否接受 `max`，仍需以实际请求结果为准；上游不支持时返回错误，不自动降低强度。

## 使用范围

- 支持 Responses 文本输入、客户端工具调用转换，以及上游事件到本地 SSE 的转换。
- 每个进程使用一份独立 OAuth 文件；没有多账号管理界面、账号轮询或模型列表接口。
- 使用固定 task 槽位池：默认每账号并发 `4`、等待队列 `64`、排队及上游启动等待预算 `30s`。这些是本地配置，不代表上游允许的并发。
- 可选 `--upstream-rps` 分别限制每账号的 WebSocket 拨号和 `response.create` 启动速率，默认 `0`（关闭限速，仍保留并发限制）。握手安全重试也计入拨号限制。
- 每次请求自带完整上下文，不跨客户端追加历史；不支持 `previous_response_id`。复用 task 槽位不会复用调用方的对话历史。
- task 身份保存在 `~/.bps-local/local-state`，重启沿用；同一状态目录同时只允许一个服务进程使用。
- 独立 CLI 的图片输入使用 HTTPS 图片 URL；不提供内联图片的公开托管地址。
- 不执行 Excel 工作簿操作，不提供上游托管的搜索、文件搜索或图像生成工具。
- OAuth 文件可保存 Access Token、Refresh Token 和有效期等元数据，但 **CLI 不自动使用 Refresh Token 刷新授权**。登录完成和 `--check` 均不验证真实生成权限。

Windows 构建使用 `go build -o bps-local.exe ./cmd/bps-local`。附带的启动脚本默认代理是 `12450`，使用 `7890` 时请显式覆盖：

```powershell
.\bps-local.exe --login --proxy http://127.0.0.1:7890
.\bps.ps1 -Proxy http://127.0.0.1:7890
```

完整参数、上下文示例及故障排查见 [使用说明](docs/USAGE.md)。上游只支持 `ws`；`--upstream-transport sse` 会被拒绝，本地 Responses 接口仍返回 SSE。

## 升级与状态目录

停止旧进程后替换二进制，保留原 OAuth 文件和 `--state-dir` 目录。首次启动会创建 `task.key` 与 `owner.lock`；后续重启请沿用它们，不要把该目录当作缓存清理。状态目录不保存 OAuth 凭据或对话历史。

需要同时启动多个实例时，为每个实例指定不同的 `--addr` 和独立的 `--state-dir`。并发和限速均在各进程内生效，不会跨进程合并。同一个状态目录被占用时，新进程会明确拒绝启动。

例如，在默认并发下启用每秒两次的启动限制：

```bash
./bps-local --proxy http://127.0.0.1:7890 --upstream-rps 2
```

发布包由 `scripts/package.sh` 仅构建 `./cmd/bps-local`，附带使用文档、许可、占位凭据示例及 Windows 启动脚本。

## 许可与来源

项目许可、第三方声明及依赖许可原文统一见根目录 [LICENSE](LICENSE)。

## 社区

- [LINUX DO](https://linux.do/) · 项目发布与交流。
