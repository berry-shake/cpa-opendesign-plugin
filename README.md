# CPA OpenDesign Plugin

CLIProxyAPI（CPA）插件：把 [OpenDesign](https://open-design.ai) 套餐（Go / Plus / Pro / Max）的 API Key 作为 CPA 认证文件接入，转发 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages。

- 每个 Key 是一个认证文件，和其他凭证一样参与 CPA 的选号、冷却和故障转移，可以单独设出口代理
- 按 Key 拉取模型列表注册到 CPA（套餐不同，模型不同），默认模型名 `opendesign/<模型 ID>`，也可以只注册选中的模型
- 请求原样转发，不做协议转换；OpenDesign 自己支持三种协议，每个模型都能用
- 在 Key 卡片上登录 OpenDesign 账号后，可以看官方 5 小时 / 7 天剩余额度和余额，CPA 额度面板也能看
- 自带管理页：Key、模型、出口代理、请求日志

> 社区插件，与 OpenDesign 官方无关。

## 安装

需要 CLIProxyAPI v8 并开启动态插件（Unix 上 CPA 要用 cgo 编译）；支持 Linux / macOS（amd64、arm64）和 Windows amd64。

**插件商店**：在 CPA 配置里加上商店源，然后在管理面板的插件商店里安装 **OpenDesign**。

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/berry-shake/CLIProxyAPI-Plugins-Store/main/registry.json
```

**手动安装**：从 [Release](https://github.com/berry-shake/cpa-opendesign-plugin/releases) 下载对应平台的 zip，把里面的 `opendesign.so`（macOS 是 `.dylib`，Windows 是 `.dll`）放到 `plugins/<系统>/<架构>/` 下，再在配置里打开 `plugins.configs.opendesign.enabled`。配置里已有 `store.version` 时，CPA 只加载版本号对得上的文件，手动换文件要同时改 `store.version` 和 `release-tag`。

## 配置

都是可选项，写在 `plugins.configs.opendesign` 下；填错的值回退默认并记一条警告。

```yaml
plugins:
  configs:
    opendesign:
      enabled: true
      base-url: https://amr-link.open-design.ai  # 末尾的 /v1 会自动去掉
      model-prefix: opendesign                   # 认证文件没写 prefix 时的默认前缀，"" = 不加
      models-refresh-minutes: 60                 # 重新拉取模型列表的间隔，0 = 不轮询
      request-timeout-seconds: 600               # 非流式请求超时
      stream-idle-timeout-seconds: 300           # 流式响应多久没数据就断开
      data-dir: plugins/opendesign-data          # 请求日志和代理列表的目录
      log-retention: 100000                      # 请求日志最多保留条数，0 = 不记录
      log-retention-days: 30                     # 请求日志保留天数，0 = 只按条数清理
```

## 认证文件

`type` 为 `opendesign` 的认证文件归本插件管理；管理页添加的 Key 保存为 `opendesign-<指纹>.json`。

```json
{
  "type": "opendesign",
  "api_key": "<OpenDesign API Key>",
  "label": "主账号",
  "models": ["deepseek-v4-pro", "glm-5.3-flash"],
  "prefix": "opendesign",
  "proxy_url": "socks5://127.0.0.1:1080"
}
```

| 字段 | 说明 |
|---|---|
| `api_key` | 必填 |
| `label` | 显示名称，默认用文件名 |
| `models` | 要注册的模型。不写或 `null` = 套餐里全部对话模型，`[]` = 不注册 |
| `prefix` | 模型名前缀。不写用配置里的 `model-prefix`，`""` = 不加前缀 |
| `proxy_url` | 出口代理，支持 `http(s)://`、`socks5(h)://`，`direct` = 强制直连；不写时依次用 CPA 的 `proxy-url`、环境变量 |
| `base_url` | 覆盖配置里的 `base-url` |
| `priority` / `note` / `disabled` | CPA 通用字段 |

`account_*` 和 `model_revision` 由插件维护，不要手改。

> CPA 的 `routing.force-model-prefix` 默认关闭，这时模型会同时注册成 `opendesign/deepseek-v4-pro` 和 `deepseek-v4-pro`，后者会和其他渠道的同名模型混在一起轮询。只想用带前缀的名字就把它打开（全局生效）。

## 工作方式

**模型**：每隔 `models-refresh-minutes` 用每个 Key 拉一次 `/v1/models`，哪个 Key 的模型有变化就只重新注册哪个；拉取失败保留上一次的结果。

**转发**：

| 客户端格式 | OpenDesign 接口 |
|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |

- 只改写请求体里的 `model`，其余原样转发；Chat 流式请求会自动加上 `stream_options.include_usage`
- OpenDesign 没有 `count_tokens`，这类请求直接返回 501
- 流式响应在第一帧真正的输出之前，上游报错（限流、过载）按普通错误处理，CPA 照常冷却或换 Key；流没收到结束帧就断了，记为 `stream_truncated`（502）

OpenDesign 的 Messages 流有几处不合协议，插件会改正：

- 停在 `max_tokens` 时缺少 `message_delta` 和 `message_stop`，插件补上
- 思考块开成 `redacted_thinking`，后面却发 `thinking_delta` / `signature_delta`（Claude Code 会因此断流、改发非流式），插件改成 `thinking` 块
- 调用了工具，结尾的 `stop_reason` 却是 `end_turn`，插件改成 `tool_use`

**上游错误**：

| OpenDesign 返回 | 交给 CPA | 效果 |
|---|---|---|
| 401 / 403 | 原状态码 | 冷却这个 Key |
| 404（模型不存在） | 400 | 只失败这一次 |
| 429（额度用完或限流） | 429，带 `Retry-After` | 按上游给的时间冷却 |
| 529 / 499 | 503 / 408 | 可重试 |

## 额度

API Key 查不到额度，插件提供两种方式：

- **登录账号**：Key 卡片上点「登录」，流程和官方 `vela login` 一样（浏览器里核对验证码并确认）。之后卡片和 CPA 额度面板显示官方 5 小时 / 7 天剩余额度和余额。账号凭据比 API Key 权限大（能查账单、付款），保存在这个 Key 的认证文件里，只发给 OpenDesign 的账号接口，不会返回给页面；「退出」会吊销它。
- **请求日志统计**：卡片显示经过本插件的近 5 小时 / 24 小时 / 7 天 / 30 天 Token 和请求数。只能看用了多少，看不到还剩多少。

额度用完时 OpenDesign 返回 429，CPA 按 `Retry-After` 冷却这个 Key。

## 管理页

在 CPA 管理面板的插件菜单里点 **OpenDesign**（`/v0/resource/plugins/opendesign/manage`）。

- **凭据**：Key 卡片，添加、编辑、启停、删除，选模型，看用量和额度
- **代理**：常用出口代理列表，编辑 Key 时直接选；可以检测出口 IP、地区和延迟
- **模型列表**：各 Key 套餐里的模型、上下文和输出上限，以及在 CPA 里注册的名字
- **请求日志**：每次调用的模型、状态、首字时间、耗时、Token 和速度，可以用 CPA 日志里的 8 位请求 ID 搜索；只记元数据，不记内容
- **配置概览**：当前生效的配置

请求日志里有两种速度，统计条取成功请求的中位数：

- **生成速度** =（输出 Token − 1）÷（总耗时 − 首字时间），只算流式请求，即 vLLM 的 1 ÷ TPOT
- **端到端速度** = 输出 Token ÷ 总耗时，包含等首字的时间，即 Artificial Analysis 的 Output Speed

数据都在 `data-dir` 下：`opendesign.db`（请求日志，SQLite）和 `proxies.json`（代理列表，含代理密码，权限 0600）。

## 管理接口

都在 `/v0/management/plugins/opendesign/` 下，CPA 先校验管理密钥再转给插件。

| 接口 | 用途 |
|---|---|
| `GET status` | 插件状态和 Key 列表 |
| `GET models`、`POST models/refresh` | 模型列表、重新拉取 |
| `POST credentials`、`PATCH credentials` | 添加、编辑 Key |
| `GET credentials/quota?auth_index=` | Key 的用量和额度 |
| `POST account/login`、`GET` / `DELETE account/login?id=`、`DELETE account?auth_index=` | 登录账号、查状态、取消、退出 |
| `GET logs`、`DELETE logs` | 查询、清空请求日志 |
| `GET` / `POST` / `PATCH` / `DELETE proxies`，`POST proxies/import`、`proxies/order`、`proxies/check` | 代理列表 |

```bash
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" -H "Content-Type: application/json" \
  -d '{"api_key":"<OpenDesign API Key>","models":["deepseek-v4-pro"],"prefix":""}' \
  http://127.0.0.1:8317/v0/management/plugins/opendesign/credentials
```

编辑只改传了的字段：`null` 恢复默认，`""` 清空。`api_key` 不能改，换 Key 就新增一个再删掉旧的。启停和删除用 CPA 原生的认证文件接口。

## 构建

需要 Go 和 Node 20。各供应商插件共用的代码（Go 和管理页的公共部分）在 [cpa-provider-core](https://github.com/berry-shake/cpa-provider-core)，版本跟着 `go.mod` 走；`make web` 会先把它的公共前端拷到 `web/src/core/`。要同时改 core，用 `go.work` 把本地的 core 加进来，或者设 `CPA_PROVIDER_CORE_DIR` 指向它。

```bash
make web    # 构建管理页（web/，React + Vite），嵌进插件
CGO_ENABLED=1 go build -buildmode=c-shared -o opendesign.so .
make web-test && go test ./...
```

推送 `v*` tag 后，GitHub Actions 跑测试、构建五个平台并发布 Release。

## License

MIT。国旗图标来自 [Twemoji](https://github.com/jdecked/twemoji)（CC-BY 4.0）。
