# BPS WebSocket 协议观测

`bps-local --protocol-diagnostic` 使用已有的独立 BPS OAuth 文件，通过指定代理向 BPS 发送最多两次短 WebSocket 请求，并保存脱敏 JSON 报告。它用于观察协议信号，不评定模型质量，也不会根据门票或模型字段差异声明账号“降智”。

该命令不启动 HTTP 监听，不占用 `8790`，不调用 CPA 管理接口，不改变 CPA 的账号启停或调度状态。它不执行设备登录，不交换 refresh token，不修改 OAuth 文件。真实运行会产生 BPS 请求，可能消耗对应账号的请求额度。

## 运行

在 `bps-local` 目录构建并运行：

```bash
go build -o bps-local ./cmd/bps-local
./bps-local --protocol-diagnostic \
  --auth "$HOME/.bps-local/oauth.json" \
  --proxy http://127.0.0.1:7890 \
  --diagnostic-output ./bps-protocol-diagnostic.json
```

`--auth` 默认使用 `BPS_AUTH_FILE`，否则读取 `~/.bps-local/oauth.json`。凭据必须属于 BPS 应用，且未过期；本地 JWT 声明校验不代表上游已授权，实际结果仍以 BPS 返回为准。

代理必须通过 `--proxy` 或 `BPS_LOCAL_PROXY` 明确配置，支持 `http`、`https`、`socks5`、`socks5h`。没有代理时拒绝运行，不自动回退直连；TLS 证书校验保持启用。

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `--protocol-diagnostic` | `false` | 执行观测后退出 |
| `--diagnostic-model` | `gpt-6-astra` | 请求模型；不代表该账号支持此模型 |
| `--diagnostic-effort` | `medium` | 请求的推理强度，可选 `none`、`low`、`medium`、`high`、`xhigh` |
| `--diagnostic-output` | `bps-protocol-diagnostic.json` | 新报告文件路径，父目录必须已经存在 |
| `--ws-idle-timeout` | `2m` | 上游 WebSocket 事件等待时间 |
| `--bps-tools-version` | 空 | 可选 BPS 工具版本，不指定时由服务端决定 |

此模式只使用 WebSocket，拒绝与 `--upstream-transport sse`、`--login`、`--check`、`--arc-text-session`、`--version`、`--auth-path` 组合。普通桥接模式的监听参数不参与此次执行。

## 两次请求的含义

1. 第一轮发送固定的短请求，观察握手、事件和完整结束状态。如果失败或未完整结束，停止，不重试，不再发送第二轮。
2. 第一轮完整成功后，用新的 WebSocket 连接发送第二个固定短请求。如果第一轮实际返回了唯一、确切的 `x-codex-turn-state`，第二轮才实验性回传该值，并仅携带第一轮的 `__cflb` / `__oailb` 路由 cookie。没有确切门票时，第二轮使用新的基线会话；不会猜测、构造或从其他字段提取门票。

短提示词内容沿用原生 Codex 探针的 `Reply with OK.`，通过正常 BPS 转换后发送；请求并非原生探针请求的逐字节复制。

每次请求均通过同一个显式代理，只连接 BPS 上游。没有自动重试、重连续传、SSE 回退或后台循环。第二轮的门票回传属于协议实验，不代表 BPS 与原生 Codex 具有相同的门票语义。

每轮另有 120 秒总时限；即使调高 `--ws-idle-timeout`，也不会取消该总时限。

## 报告与退出状态

报告使用 `0600` 权限创建，并通过 `O_EXCL` 拒绝覆盖现有文件，包括 OAuth 文件和符号链接。重复运行时请使用新的 `--diagnostic-output` 路径；输出文件已存在时，在发送诊断请求前退出。

终端只显示报告路径、完成或失败的固定摘要，以及“仅观测，不改变账号状态”。不会打印凭据、代理用户名密码或原始上游错误。JSON 中只保留允许的观测字段；敏感门票和会话标识以是否存在、长度或 HMAC 摘要等方式描述，不保存原值。Header 和事件名称也只保留允许名单中的原名，未知名称仅记录长度及本次运行专用的 HMAC 摘要。

运行器发生错误时仍尝试写入已收集的报告，并以非零状态退出。命令行参数、凭据文件加载或报告路径的前置检查失败时，可能尚未生成报告。报告写入失败同样返回非零状态；不会覆盖已有文件来补救。

报告里的请求模型、事件完成状态、门票是否出现、哈希是否相同等仅是观测结果。无门票不等于正常，有不同门票也不等于降智；上游响应模型字段与请求模型不同也不能单独证明能力下降。此报告不会自动得出 `healthy` / `degraded` 结论，不改变账号状态。

## 凭据已过期或被上游拒绝时

过期凭据在发送 BPS 请求前被拒绝。凭据读取返回过期错误时，报告显示 `diagnostic_credentials_expired`；JWT 或凭据源记录的到期时间已过期、缺失或无法确定时，显示 `diagnostic_credentials_expired_or_unknown`。命令不会尝试已有 refresh token，也不会自动开始登录。

本地有效期检查通过后，上游仍可能拒绝凭据。如果报告显示 `handshake_status: 401`，说明 WebSocket 握手阶段没有通过认证，尚未取得生成事件；这不构成模型质量结论，也不能仅凭本地有效期推断凭据仍可用。此时停止后续请求，可重新独立授权后对照测试。

如需重新授权，请单独执行明确的设备登录命令，并选择尚不存在的新 OAuth 文件：

```bash
./bps-local --login \
  --auth "$HOME/.bps-local/oauth-diagnostic-new.json" \
  --proxy http://127.0.0.1:7890
```

在浏览器中由账号持有人完成授权后，再用该新路径运行诊断，并选择新的报告路径。登录成功只表示取得凭据，不代表 BPS 生成权限已验证；原 OAuth 文件和已有报告均不会被覆盖。
