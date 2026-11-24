# bps-local 使用说明

本文介绍独立 `cmd/bps-local`，不涉及 CPA 插件、管理界面或其他服务。上游使用产品内部的 BPS 接口，本文说明源码行为，不保证当前账号或接口可用。

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

下载依赖可能需要网络。本地构建不要求目录名称与 Go module 路径一致。

## 2. 独立授权

支持设备码和普通浏览器两种独立授权方式。`--login` 默认保持设备码模式；`--login-method` 只能与 `--login` 一起使用。

### 设备码登录

```bash
./bps-local --login --proxy http://127.0.0.1:7890
```

终端输出登录地址和设备码，并尝试打开浏览器。由你在浏览器完成授权，程序最多等待 15 分钟。也可显式添加 `--login-method device`。

无桌面 Linux 或需要在另一台电脑完成授权时：

```bash
./bps-local --login --no-browser --proxy http://127.0.0.1:7890
```

在自己的电脑打开终端输出的网址并输入设备码，保持服务器上的命令运行，等待凭据保存完成。

### 普通浏览器授权

本机需要已安装 Chrome、Chromium 或 Edge，并具有可显示浏览器窗口的桌面环境：

```bash
./bps-local --login --login-method browser \
  --auth "$HOME/.bps-local/browser-oauth.json" \
  --proxy http://127.0.0.1:7890
```

程序使用独立临时 profile 打开授权页面，由你输入账号、密码或验证码，选择工作区并确认授权。它通过 PKCE 完成独立授权，自动捕获本次授权回调并保存凭据，不需要设备码，也不需要安装或打开 Excel。当前仅支持授权站点内的登录流程；Google、Microsoft 等需要跳转到外部站点的第三方 SSO 请使用设备码方式。

浏览器不会读取已有 Chrome、Excel 或 Codex 的登录信息。结束时只关闭本次启动的浏览器并删除临时 profile；原有浏览器窗口和授权文件不受影响。此模式使用 BPS 注册的 HTTPS 回调，并非监听本机 OAuth 回调端口。不要把它与 `--no-browser` 一起使用。

若自动发现浏览器失败，添加 `--browser-path`，例如：

```bash
./bps-local --login --login-method browser \
  --browser-path /usr/bin/chromium \
  --auth "$HOME/.bps-local/browser-oauth.json" \
  --proxy http://127.0.0.1:7890
```

`--browser-path` 应指向本机实际安装的浏览器可执行文件。通过 `--proxy` 或 `BPS_LOCAL_PROXY` 指定代理后，浏览器和 Token 交换使用该代理；两者均未配置时，浏览器授权全流程直连，不自动推断系统代理。浏览器模式仅接受不含用户名密码的 HTTP、HTTPS 或 SOCKS 代理 URL。需要使用带认证的上游代理时，先配置本机代理转发，再把本机无认证地址传给 CLI。

### 凭据保存与检查

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

`--proxy` 优先于环境变量，支持 `http`、`https`、`socks5`、`socks5h` 地址。浏览器授权模式另要求代理 URL 不含用户名密码。登录和运行服务是两个独立命令，需要分别配置代理；代理地址不会因为一次登录而自动成为后续启动的配置。需要固定代理出口时，请在这两个命令中都显式指定。

使用指定授权文件和端口：

```bash
./bps-local --auth "$HOME/.bps-local/account-2.json" \
  --addr 127.0.0.1:8791 --state-dir "$HOME/.bps-local/account-2-state" \
  --proxy http://127.0.0.1:7890
```

服务只允许绑定回环地址，不接受 `0.0.0.0`。本地端点没有 API Key 校验，应在可信的本机环境使用。按 `Ctrl+C` 停止服务。多个实例必须使用不同的监听地址和独立状态目录，详见第 5 节。

Windows 附带脚本 `bps.ps1` 的默认代理为 `http://127.0.0.1:12450`。如果代理实际监听 `7890`，应这样启动：

```powershell
.\bps.ps1 -Proxy http://127.0.0.1:7890
.\bps.ps1 -Status
```

需要自定义 OAuth 文件时，可直接调用 `.\bps-local.exe --auth ... --proxy ...`。`bps-codex.ps1` 是配套的本地客户端启动脚本，需要另外安装客户端并先启动桥接器，其参数可直接查看脚本。

## 4. 请求与会话

本地支持 `POST /v1/responses`，兼容路径为 `POST /responses`。成功建立流后返回 `text/event-stream`，即使请求包含 `stream:false` 也仍返回 SSE。此 CLI 不提供 `/v1/chat/completions`、`/v1/models` 或非流式 JSON 聚合响应。

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

从 CLI `0.4.0` 起，`gpt-6.2-astra` 和 `gpt-6.2-sol` 使用以下推理强度规则。模型 ID 始终原样转发，不做模型别名或失败回退。例如：

```bash
curl -N http://127.0.0.1:8790/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6.2-astra","input":"只回复 OK","stream":true,"reasoning":{"effort":"medium"}}'
```

| 请求中的 `reasoning.effort` | 这两个 6.2 模型实际发送的值 |
|---|---|
| 未填写或空值 | `medium` |
| `low`、`medium`、`high`、`xhigh`、`max` | 原样发送 |
| `none`、`minimal` | `low` |
| `x-high`、`extra-high`、`extra_high` | `xhigh` |
| `ultra` | 本地拒绝；如需 `max`，请显式填写，不自动降档 |

其他不支持的强度值也会被拒绝。上述规则只按这两个精确模型 ID 匹配；其他模型保留原有行为，包括将 `max`、`ultra` 映射为 `xhigh`，不新增模型白名单。CLI 没有模型列表接口，也不会根据账号授权自动选择模型。

本次测试仅确认本地转换及 mock WebSocket 请求中的模型与强度，未验证真实 BPS 账号权限或上游 `max` 支持情况。上游不支持所请求的模型或强度时，错误会返回给调用方，不改用其他模型或较低强度。

官方 API 的上下文和工具能力不能直接视为 BPS 的能力；本轮仍沿用 `context_management` 默认值 `200000` 及现有工具转换规则。

链路为：本地 HTTP 请求 → 等待固定 task 槽位 → 转换为 BPS 请求 → 独立的上游 WebSocket → 转换为 Responses SSE 事件。上游只支持 WebSocket，旧 `--upstream-transport sse` 会被拒绝。每次本地请求建立自己的上游 WebSocket；发送生成请求后不会自动重连并重发。发送前的少数瞬时握手错误可安全重试，规则见排查表。

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

非空的 `previous_response_id` 会被拒绝，必须直接提供展开后的完整历史。每次请求只使用本次携带的上下文，不会自动追加其他客户端的上一轮文本或工具历史。客户端的 `session_id` 不用于选择上游 task 或共享历史；即使两个请求复用同一 task 槽位，也不会合并对话。

客户端工具调用会转换成 BPS 可承载的形式并还原为 Responses 工具事件；工具实际执行仍由客户端负责。搜索、文件搜索、图像生成等上游托管工具会被过滤。独立 CLI 可接受 HTTPS 图片 URL，但没有配置图片中转服务，所以不能直接使用 base64/data URL 或文件 ID 图片。

请求负载由调用方控制。上游返回 `request_rejected` 等错误时，CLI 将错误事件返回给调用方。

## 5. 并发、启动限速与持久状态

默认每账号最多同时运行 `4` 个请求，对应 `4` 个固定 task 槽位。请求取得空闲槽位后，一直持有到生成和连接清理结束；槽位可以供后续请求复用。忙碌时最多允许 `64` 个请求等待，超出后返回 HTTP 429 `bps_queue_full`。`--max-pending 0` 关闭排队，忙碌时直接拒绝新请求。

`--queue-timeout 30s` 是从进入队列起计算的排队及启动限速等待预算。取得槽位不会重置预算；启用 `--upstream-rps` 后，每次拨号（包括握手重试）和 `response.create` 的门控等待都受剩余预算约束。关闭启动限速时不产生这部分等待。此预算不截断已经开始的握手或生成；握手另有共享的 60 秒时限，`--ws-idle-timeout` 单独控制等待下一条上游事件的最长时间。

`--upstream-rps` 默认 `0`，表示关闭启动限速，并发槽位仍然生效。设置为正整数时，每账号分别限制任意滚动 1 秒内的 WebSocket 拨号次数和 `response.create` 发送次数。每次拨号尝试（包括安全重试）均计数；一次生成不会因为经过两个门控就占用同一个计数两次。`--rate-wait-timeout` 限制单次启动门控等待时间，同时受剩余 `--queue-timeout` 约束；`--rate-max-pending` 限制等待启动的数量，设为 `0` 使用默认 `64`，不表示关闭排队。

例如，允许最多 8 个活动请求，同时将拨号与生成启动各自限制为每秒 2 次：

```bash
./bps-local --proxy http://127.0.0.1:7890 \
  --max-concurrent 8 --max-pending 64 --queue-timeout 30s \
  --upstream-rps 2 --rate-wait-timeout 30s --rate-max-pending 64
```

这些参数是本地资源控制，不代表已验证的上游并发或速率额度；提高数值不保证上游会接受更多请求。

状态目录按 `--state-dir`、`BPS_LOCAL_STATE_DIR`、`~/.bps-local/local-state` 的顺序选取。首次运行服务会在其中创建 `task.key` 和 `owner.lock`，用于维持 task 身份并独占目录。状态不包含 OAuth 凭据、对话历史或刷新状态。登录、`--check` 和协议诊断模式不创建或占用该目录。

升级时先停止旧进程，替换二进制，再沿用原状态目录。正常重启会保留相同账号及槽位的 task 身份。不要删除或重建 `task.key`；已有 `owner.lock` 却缺少 key 时程序会拒绝启动，应恢复原 key。非 Windows 系统要求目录权限为 `0700`、文件为 `0600`；Windows 沿用父目录 ACL，并检查普通文件、符号链接和独占锁。

同一个状态目录只允许一个服务进程持有。`owner.lock` 在停止后仍会保留，它的存在不代表进程仍在运行，也无需手动删除。多实例应使用不同监听端口和独立创建的状态目录，不要复制同一份 `task.key` 给并行实例。并发队列和速率限制均在进程内维护，不会跨实例合并。

## 6. 参数参考

| 参数 | 默认值 / 用途 |
|---|---|
| `--login` | 执行独立授权并保存到一个尚不存在的 OAuth 文件，默认使用设备码 |
| `--login-method` | `device`；可选 `browser`，仅与 `--login` 一起使用 |
| `--browser-path` | 浏览器授权使用的 Chrome、Chromium 或 Edge 可执行文件路径；省略时自动发现 |
| `--no-browser` | 设备码登录只打印 URL 和设备码，不自动打开浏览器；不支持 browser 模式 |
| `--auth` | OAuth 文件；未指定时读取 `BPS_AUTH_FILE` 或 `~/.bps-local/oauth.json` |
| `--auth-path` | 输出选中的 OAuth 路径后退出，不读取凭据 |
| `--check` | 检查本地凭据信息，不验证上游生成 |
| `--addr` | `127.0.0.1:8790`，仅允许回环地址 |
| `--proxy` | 默认读取 `BPS_LOCAL_PROXY` |
| `--upstream-transport` | `ws`，唯一支持的上游传输；旧 `sse` 会被拒绝 |
| `--ws-idle-timeout` | `2m`，等待下一条上游 WS 事件的最长时间，不是总生成时长 |
| `--max-concurrent` | `4`，每账号活动请求上限及固定 task 槽位数，范围 `1–10000` |
| `--max-pending` | `64`，等待 task 槽位的请求上限；`0` 关闭排队 |
| `--queue-timeout` | `30s`，排队及上游启动等待的总预算，必须大于 `0` |
| `--state-dir` | `BPS_LOCAL_STATE_DIR` 或 `~/.bps-local/local-state`，须在重启时保留 |
| `--upstream-rps` | `0`，关闭启动限速；正整数分别限制每账号滚动 1 秒的 WS 拨号和 `response.create`，最大 `10000` |
| `--rate-wait-timeout` | `30s`，单次启动门控等待上限；`0` 使用默认值 |
| `--rate-max-pending` | `64`，每账号等待启动的数量上限；`0` 使用默认值 |
| `--bps-tools-version` | 空，使用上游默认工具版本；可显式指定版本 ID |
| `--arc-text-session` | 默认关闭，实验性 ARC 文本初始化，不接受客户端工具 |
| `--version` | 打印构建版本后退出 |
| `--protocol-diagnostic` | 经显式代理执行两次短 WS 诊断请求，保存报告后退出 |
| `--diagnostic-model` | `gpt-6-astra`，诊断请求指定的模型，不代表账号一定可用 |
| `--diagnostic-effort` | `medium`，诊断推理强度；`max` 仅用于 `gpt-6.2-astra` 或 `gpt-6.2-sol` |
| `--diagnostic-output` | `bps-protocol-diagnostic.json`，必须是尚不存在的报告文件 |

诊断模式需要 `--proxy` 或 `BPS_LOCAL_PROXY`，会产生真实请求和用量；不能与登录、本地检查、ARC、版本输出或路径输出模式组合。它只记录协议观测，不判断账号是否“降智”，不改变账号状态。日常使用无需运行。

## 7. 排查

| 现象 | 检查方向 |
|---|---|
| `bind: address already in use` | 原有进程已占用端口；使用另一个 `--addr` 或先停止已确认的旧进程 |
| `OAuth destination exists` | 使用新的 `--auth` 路径，登录不会覆盖旧文件 |
| 浏览器授权无法启动 | 确认已安装 Chrome、Chromium 或 Edge，并使用桌面环境；可用 `--browser-path` 指定可执行文件。无桌面服务器使用设备码模式加 `--no-browser` |
| 浏览器授权拒绝代理配置 | 代理 URL 不应含用户名密码；带认证的上游代理需先配置本机转发，再使用本机无认证代理地址 |
| 状态目录已被占用 | 停止持有该目录的旧服务，或为另一个实例指定独立 `--state-dir`；仅修改端口不够，不要删除正在使用的 `owner.lock` |
| task key 缺失、无效或权限错误 | 恢复该状态目录原有的 `task.key` 并检查权限；不要用新 key 覆盖旧身份 |
| `bps_queue_full` / `bps_queue_timeout` | 本地槽位等待队列已满或等待预算耗尽；减少并发调用，检查请求是否长时间占用槽位 |
| `bps_rate_queue_full` / `bps_rate_wait_timeout` | 本地启动限速等待队列已满或等待超时；检查 `--upstream-rps`、等待预算和调用速率 |
| `basispoints_auth_unavailable` / AT 过期 | 检查当前 OAuth 路径与有效期，重新授权后使用新文件 |
| WS 握手失败 | 普通桥接仅对发送 `response.create` 之前的 EOF、连接重置和瞬时超时最多尝试 3 次，所有尝试共享同一个 60 秒握手预算，并响应请求取消；错误包含安全的 `reason` / `attempts`。开启启动限速时每次拨号均计数，门控等待受剩余启动等待预算约束。HTTP 拒绝、认证/证书错误不自动重试；发送请求后不重放。诊断模式仍仅尝试一次。持续失败请检查明确配置的代理、网络可达性、授权及上游兼容性 |
| `basispoints_ws_timeout` | 等待下一条上游事件超时；检查代理与上游，可按实际需求调整超时时间 |
| HTTP 200 后出现 `error` 事件 | 流已建立但生成失败，应读取事件体；不能只按 HTTP 状态判断成功 |
| `request_rejected` | 上游拒绝了请求；该错误本身不能证明具体并发上限、固定请求次数或封控原因 |

`GET /healthz` 仅反映本地服务和凭据读取情况，不会请求上游，不能替代真实生成验证。分享日志或报告前，请确认其中不包含授权文件、Token、代理密码或私人提示词。
