# bps-local 使用说明

本文对应 **2026-11-06 的 `8736c9f` 源码快照**。这里只介绍独立 `cmd/bps-local`，不涉及 CPA 插件、管理界面或其他服务。上游使用产品内部的 BPS 接口，本文说明源码行为，不保证当前账号或接口可用。

## 1. 构建

准备 Go 1.26.0 或更高版本，在源码根目录执行：

```bash
go build -o bps-local ./cmd/bps-local
./bps-local --help
```

Windows PowerShell：

```powershell
go build -o bps-local.exe ./cmd/bps-local
.\bps-local.exe --help
```

下载依赖可能需要网络。源码保留快照原有的 Go module 路径，本地构建不要求目录名称与 module 路径一致。

## 2. 独立授权

通过自己的 BPS 设备授权流程获取凭据：

```bash
./bps-local --login --proxy http://127.0.0.1:7890
```

终端输出登录地址和设备码，并尝试打开浏览器。由你在浏览器完成授权，程序最多等待 15 分钟。没有可用浏览器时，用 `--no-browser` 只打印地址，然后手动打开。

OAuth 文件路径的优先级为：`--auth` 参数、`BPS_AUTH_FILE` 环境变量、`~/.bps-local/oauth.json`。例如，为另一个账号使用独立文件：

```bash
./bps-local --login --auth "$HOME/.bps-local/account-2.json" \
  --proxy http://127.0.0.1:7890
```

登录命令不会覆盖已存在的文件。请保管好授权文件，不要提交到代码仓库或公开分享。

文件不仅包含 Access Token（AT）与可能返回的 Refresh Token（RT），还保存授权响应提供的 `token_type`、`expires_in`、`scope`、取得时间、应用 `client_id` 等信息；部分字段可能缺省。`client_id` 表示注册应用，不是每个账号随机生成的身份标识。

本程序不自动读取 Codex 授权文件，也不自动用 RT 换取新 AT。AT 过期后，需要通过正常授权流程取得新的独立文件，并使用 `--auth` 切换。服务会在请求时定期重新读取所选文件，但重新读取文件不等于 OAuth 刷新。

只检查本地文件信息：

```bash
./bps-local --auth-path
./bps-local --check --auth "$HOME/.bps-local/account-2.json"
```

`--check` 不发起生成请求。它打印的账号、有效期和 scope 是本地读取或解码的结果，不能证明 BPS 模型权限；授权成功后的 “generation not yet verified” 也表示还未做真实生成验证。

## 3. 代理与启动

启动默认端口 `127.0.0.1:8790`：

```bash
./bps-local --proxy http://127.0.0.1:7890
```

或通过环境变量指定代理：

```bash
BPS_LOCAL_PROXY=http://127.0.0.1:7890 ./bps-local
```

`--proxy` 优先于环境变量，支持 `http`、`https`、`socks5`、`socks5h` 地址。登录和运行服务是两个独立命令，需要分别配置代理；代理地址不会因为一次登录而自动成为后续启动的配置。需要固定代理出口时，请在这两个命令中都显式指定。

使用指定授权文件和端口：

```bash
./bps-local --auth "$HOME/.bps-local/account-2.json" \
  --addr 127.0.0.1:8791 --proxy http://127.0.0.1:7890
```

服务只允许绑定回环地址，不接受 `0.0.0.0`。本地端点没有 API Key 校验，应在可信的本机环境使用。按 `Ctrl+C` 停止服务。

Windows 附带脚本 `bps.ps1` 的默认代理为 `http://127.0.0.1:12450`。如果代理实际监听 `7890`，应这样启动：

```powershell
.\bps.ps1 -Proxy http://127.0.0.1:7890
.\bps.ps1 -Status
```

需要自定义 OAuth 文件时，可直接调用 `.\bps-local.exe --auth ... --proxy ...`。`bps-codex.ps1` 是配套的本地客户端启动脚本，需要另外安装客户端并先启动桥接器，其参数可直接查看脚本。

## 4. 请求与会话

本地支持 `POST /v1/responses`，兼容路径为 `POST /responses`。成功建立流后返回 `text/event-stream`。此 CLI 不提供 `/v1/chat/completions`、`/v1/models` 或非流式 JSON 聚合响应。

将以下 `YOUR_MODEL` 替换为账号有权使用的模型：

```bash
curl -N http://127.0.0.1:8790/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "YOUR_MODEL",
    "input": "只回复 OK",
    "stream": true,
    "reasoning": {"effort": "medium"}
  }'
```

默认链路为：本地 HTTP 请求 → 转换为 BPS 请求 → 独立的上游 WebSocket → 转换为 Responses SSE 事件。每次本地请求建立自己的上游 WebSocket；连接异常不会自动重连并重发生成。

客户端负责保留并发送需要的上下文。例如第二轮请求包含第一轮的提问与回答：

```json
{
  "model": "YOUR_MODEL",
  "stream": true,
  "input": [
    {"role": "user", "content": "记住数字 7。"},
    {"role": "assistant", "content": "好的，数字是 7。"},
    {"role": "user", "content": "刚才的数字是多少？"}
  ]
}
```

非空的 `previous_response_id` 会被拒绝，必须直接提供展开后的历史。本快照根据当前请求中的上下文等信息生成 BPS 元数据，不会自动追加其他客户端的上一轮文本。

客户端工具调用会转换成 BPS 可承载的形式并还原为 Responses 工具事件；工具实际执行仍由客户端负责。搜索、文件搜索、图像生成等上游托管工具会被过滤。独立 CLI 可接受 HTTPS 图片 URL，但没有配置图片中转服务，所以不能直接使用 base64/data URL 或文件 ID 图片。

请求负载由调用方控制。上游返回 `request_rejected` 等错误时，CLI 将错误事件返回给调用方。

## 5. 参数参考

| 参数 | 默认值 / 用途 |
|---|---|
| `--login` | 执行设备授权并保存到一个尚不存在的 OAuth 文件 |
| `--no-browser` | 登录时只打印 URL，不自动打开浏览器 |
| `--auth` | OAuth 文件；未指定时读取 `BPS_AUTH_FILE` 或 `~/.bps-local/oauth.json` |
| `--auth-path` | 输出选中的 OAuth 路径后退出，不读取凭据 |
| `--check` | 检查本地凭据信息，不验证上游生成 |
| `--addr` | `127.0.0.1:8790`，仅允许回环地址 |
| `--proxy` | 默认读取 `BPS_LOCAL_PROXY` |
| `--upstream-transport` | `ws`；源码也保留旧 `sse` 模式，建议保持 `ws` |
| `--ws-idle-timeout` | `2m`，等待下一条上游 WS 事件的最长时间，不是总生成时长 |
| `--bps-tools-version` | 空，使用上游默认工具版本；可显式指定版本 ID |
| `--arc-text-session` | 默认关闭，实验性 ARC 文本初始化，不接受客户端工具 |
| `--version` | 打印构建版本后退出 |
| `--protocol-diagnostic` | 经显式代理执行两次短 WS 诊断请求，保存报告后退出 |
| `--diagnostic-model` | `gpt-6-astra`，诊断请求指定的模型，不代表账号一定可用 |
| `--diagnostic-effort` | `medium`，诊断推理强度 |
| `--diagnostic-output` | `bps-protocol-diagnostic.json`，必须是尚不存在的报告文件 |

诊断模式需要 `--proxy` 或 `BPS_LOCAL_PROXY`，会产生真实请求和用量；不能与登录、本地检查、ARC、版本输出或路径输出模式组合。它只记录协议观测，不判断账号是否“降智”，不改变账号状态。日常使用无需运行。

## 6. 排查

| 现象 | 检查方向 |
|---|---|
| `bind: address already in use` | 原有进程已占用端口；使用另一个 `--addr` 或先停止已确认的旧进程 |
| `OAuth destination exists` | 使用新的 `--auth` 路径，登录不会覆盖旧文件 |
| `basispoints_auth_unavailable` / AT 过期 | 检查当前 OAuth 路径与有效期，重新授权后使用新文件 |
| WS 握手失败 | 检查明确配置的代理、网络可达性、授权及上游兼容性 |
| `basispoints_ws_timeout` | 等待下一条上游事件超时；检查代理与上游，可按实际需求调整超时时间 |
| HTTP 200 后出现 `error` 事件 | 流已建立但生成失败，应读取事件体；不能只按 HTTP 状态判断成功 |
| `request_rejected` | 上游拒绝了请求；该错误本身不能证明具体并发上限、固定请求次数或封控原因 |

`GET /healthz` 仅反映本地服务和凭据读取情况，不会请求上游，不能替代真实生成验证。分享日志或报告前，请确认其中不包含授权文件、Token、代理密码或私人提示词。
