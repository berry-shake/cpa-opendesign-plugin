# CPA OpenDesign Plugin

一个 CLIProxyAPI（CPA）插件：**把 [OpenDesign](https://open-design.ai) Go 套餐的 API Key 作为 CPA 认证文件接入，转发 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三种协议。**

- 每个 OpenDesign API Key 是一个认证文件（`{"type":"opendesign","api_key":"..."}`），和其他凭证一样参与 CPA 的选号、冷却和故障转移；
- 用每个 Key 拉取它的模型列表（`GET /v1/models`，套餐不同列出的模型不同）并注册到 CPA，带上下文长度、单次输出上限和输入类型；前缀走 CPA 通用的 `prefix` 机制，默认 `opendesign/<模型 ID>`；每个 Key 可以只注册选中的模型；
- 请求按客户端格式原样转发到 OpenDesign 对应的接口，不做协议转换（OpenDesign 自己会在三种协议之间转换，所以每个模型三种协议都能调）；
- OpenDesign 不向 API Key 提供额度查询，管理页按插件自己的请求日志统计每个 Key 近 5 小时 / 24 小时 / 7 天 / 30 天的请求数和 Token；也可以在 Key 卡片上登录 OpenDesign 账号（和官方 `vela login` 一样的设备授权），显示官方的 5 小时 / 7 天剩余额度和余额；套餐限额用完时 OpenDesign 返回 429，CPA 按 `Retry-After` 冷却这个 Key；
- 每个 Key 可以单独配置出口代理（CPA 通用认证文件字段 `proxy_url`）；
- 自带管理页：添加、编辑、启停、删除 Key，选择上游模型，查看用量，管理代理列表，查看请求日志。

> 这是社区插件，与 OpenDesign 官方无关。OpenDesign 名称和图标归 OpenDesign 所有，这里只用来标识所接入的服务。

## 环境要求

- CLIProxyAPI v8（按 v8.0.58 的插件 SDK 构建），且开启了动态插件支持
- Linux amd64/arm64、macOS amd64/arm64 或 Windows amd64
- Unix 平台上需要启用 cgo 编译的 CPA
- 一个订阅了 OpenDesign 套餐（Go / Plus / Pro / Max）的账号，以及它的 API Key

## 安装

### 通过插件商店

在 CPA 配置里加上 berry-shake 的商店源：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/berry-shake/CLIProxyAPI-Plugins-Store/main/registry.json
```

然后在管理面板的插件商店里找到 **OpenDesign**（ID `opendesign`）安装，也可以直接调用管理接口：

```bash
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"version":"0.1.0"}' \
  http://127.0.0.1:8317/v0/management/plugin-store/opendesign/install
```

### 手动安装

1. 从 [Release](https://github.com/berry-shake/cpa-opendesign-plugin/releases) 下载对应平台的 zip，解压出 `opendesign.<so|dylib|dll>`。
2. 放进对应平台的插件目录，例如：

   ```text
   plugins/linux/amd64/opendesign.so
   plugins/linux/arm64/opendesign.so
   plugins/darwin/amd64/opendesign.dylib
   plugins/darwin/arm64/opendesign.dylib
   plugins/windows/amd64/opendesign.dll
   ```

3. 在 `config.yaml` 里启用：

   ```yaml
   plugins:
     enabled: true
     configs:
       opendesign:
         enabled: true
   ```

> 如果 `config.yaml` 里已经有 `store.version`，CPA 只会加载文件名版本和它一致的插件文件。手动换文件时记得同时改 `store.version` 和 `release-tag`。

## 插件配置

所有配置项都是可选的，写在 `plugins.configs.opendesign` 下面：

```yaml
plugins:
  configs:
    opendesign:
      enabled: true
      base-url: https://amr-link.open-design.ai   # 推理接口地址，末尾的 /v1 会自动去掉
      model-prefix: opendesign                # 认证文件没写 prefix 时使用的默认前缀；写 "" 表示不加前缀
      models-refresh-minutes: 60          # 重新拉取各 Key 模型列表的间隔（分钟），0 = 不轮询，只按需加载
      request-timeout-seconds: 600        # 非流式请求超时
      stream-idle-timeout-seconds: 300    # 流式响应多久没有新数据就中断
      data-dir: plugins/opendesign-data       # 插件数据目录（请求日志数据库 opendesign.db、代理列表 proxies.json），相对路径按 CPA 的工作目录算
      log-retention: 100000               # 请求日志最多保留多少条（上限 1000000），0 = 不记录
      log-retention-days: 30              # 请求日志保留多少天，0 = 不按天数清理，只受条数限制
```

非法的值会被忽略并回退到默认值，同时写一条警告日志，不会让插件注册失败。

OpenDesign 文档里 OpenAI 两种协议的地址是 `https://amr-link.open-design.ai/v1`，Anthropic 的是 `https://amr-link.open-design.ai`，填哪个都可以。

## 认证文件

放在 CPA 认证目录里的 JSON 文件，`type` 为 `opendesign` 的文件由本插件接管。通过管理页添加的 Key 会保存为 `opendesign-<Key 指纹>.json`，文件名不包含 Key 本身。

```json
{
  "type": "opendesign",
  "api_key": "<OpenDesign API Key>",
  "label": "主账号",
  "models": ["deepseek-v4-pro", "glm-5.3-flash"],
  "prefix": "opendesign",
  "proxy_url": "socks5://127.0.0.1:1080",
  "priority": 0,
  "note": "",
  "disabled": false
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `type` | 是 | 固定为 `opendesign` |
| `api_key` | 是 | OpenDesign API Key（也接受 `api-key`、`key`） |
| `label` | 否 | 显示名称；为空时用认证文件名（不带 `.json`） |
| `models` | 否 | 要注册的上游模型 ID 列表。**不写或为 `null` 表示注册这个 Key 的套餐里的全部对话模型**；`[]` 表示一个都不注册 |
| `prefix` | 否 | CPA 通用的模型前缀字段。不写时使用插件配置的 `model-prefix`；`""` 表示这个 Key 不加前缀 |
| `proxy_url` | 否 | 出口代理，见下文 |
| `base_url` | 否 | 覆盖插件配置里的 `base-url` |
| `priority` | 否 | CPA 通用的凭证优先级 |
| `note` | 否 | 备注 |
| `disabled` | 否 | CPA 通用的停用开关 |
| `model_revision` | — | 插件自己维护的模型列表版本标记，不要手动修改 |

### 模型前缀

前缀使用 CPA 通用的 `prefix` 字段，由 CPA 自己加到注册的模型名上，转发前再去掉。前缀为 `opendesign` 时，客户端请求 `opendesign/deepseek-v4-pro`，OpenDesign 收到的是 `deepseek-v4-pro`。

- 认证文件没写 `prefix` 时，插件把配置里的 `model-prefix`（默认 `opendesign`）作为这个 Key 的前缀交给 CPA，不会写进文件；改了 `model-prefix` 后插件会重新保存所有 OpenDesign 认证文件，让 CPA 按新前缀重新注册。
- 写了 `prefix` 就用文件里的值，`""` 表示不加前缀。在管理页或 CPA 面板里修改认证文件的 `prefix`，效果相同。

> CPA 的 `routing.force-model-prefix` 默认是 `false`。这时 CPA 会同时注册带前缀和不带前缀的两个名字（`opendesign/deepseek-v4-pro` 和 `deepseek-v4-pro`），OpenDesign 的 Key 也会进入同名模型的公共号池，和其他渠道一起轮询。如果只想通过 `opendesign/...` 访问 OpenDesign，在 CPA 配置里打开：
>
> ```yaml
> routing:
>   force-model-prefix: true
> ```
>
> 这是全局开关，打开后其他配置了 `prefix` 的凭证也只会注册带前缀的名字。管理页的"注册到 CPA 的模型名"预览会按这个开关显示。

选中的模型如果不在这个 Key 的套餐里（比如换了套餐或上游下线了），仍然会注册，管理页会把它标成"不在这个 Key 的套餐里"。

### 出口代理

`proxy_url` 是 CPA 认证文件的通用字段，本插件的所有上游请求（对话、模型列表、Key 校验）都按以下顺序选择出口：

1. 认证文件里的 `proxy_url`；
2. CPA 全局配置的 `proxy-url`；
3. 环境变量 `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`。

支持 `http://`、`https://`、`socks5://`、`socks5h://`，可以带用户名密码。写 `direct` 或 `none` 表示这个 Key 强制直连，不走全局代理。管理页里显示的代理地址会隐藏密码。

常用的代理可以在管理页的 **代理** 页里存成列表，编辑 Key 时直接从列表里选。选列表里的代理，实际上就是把它的地址写进 `proxy_url`，CPA 那边看到的还是普通的认证文件字段。

## 模型同步

- OpenDesign 的模型列表 `<base-url>/v1/models` 需要 API Key，不同套餐列出的模型不同（Plus 及以上多出 Top 模型）。插件用每个 Key（经过它自己的出口代理）分别拉取，记下每个模型出现在哪些 Key 的列表里；添加 Key 时的校验也就是拉一次它的列表。
- 列表里每个模型带上下文长度、单次请求可用的上下文和输出上限、输入类型（文本 / 图片 / 文件）、成本档位和能力档位；"全部模型"只注册启用中、能输出文本的模型。
- 插件启动后按 `models-refresh-minutes` 重新拉取所有 Key 的列表（接口不带 ETag，每次都完整拉取），删掉的 Key 的列表随之丢弃。某个 Key 的模型增减、上下文长度或输入类型变化时，插件只重新保存这个 Key 的认证文件，CPA 随即重新注册它的模型；别的 Key 不受影响。
- 拉取失败时保留上一次的列表，不会把已注册的模型清空。

## 请求转发

| 客户端格式 | OpenDesign 接口（相对 `base-url`） |
|---|---|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| OpenAI Responses | `POST /v1/responses` |
| Anthropic Messages | `POST /v1/messages` |

- 所有接口都用 `Authorization: Bearer <api_key>` 鉴权，客户端自己的鉴权头不会转发。
- 请求体只改写顶层的 `model` 字段，其余字节原样转发；
- Chat Completions 流式请求会自动加上 `stream_options.include_usage`，让 CPA 能统计用量；
- `anthropic-version` / `anthropic-beta` 只转发给 Messages 接口，客户端没带 `anthropic-version` 时补 `2023-06-01`；
- OpenDesign 没有 `count_tokens` 接口，CPA 发来的 Token 计数请求直接返回 501 `unsupported_operation`。

流式响应：

- 开头几帧（`message_start`、`response.created`、只带角色的 chunk、ping）先攒着，等到第一帧真正的输出再一起发给 CPA。这期间如果 OpenDesign 在流里报错（限流、过载等），这次调用按对应状态码失败，CPA 会和 HTTP 报错一样冷却 Key 或换 Key 重试；攒的内容超过 64KB 就不再等。
- 已经开始输出以后的错误帧原样转给客户端。
- 流没收到结束帧就断了（Chat 的 `[DONE]` 或 `finish_reason`、Responses 的 `response.completed`、Messages 的 `message_stop`），按失败处理，错误码 `stream_truncated`（502）。CPA 对这个错误码不冷却 Key。
- 例外：OpenDesign 的 Messages 流在输出停在 `max_tokens` 时，最后一个内容块结束后就直接断开，不发 `message_delta` 和 `message_stop`（自然结束时会发）。插件遇到"流正常结束、所有内容块都已关闭、只缺结束帧"的 Messages 流，并且请求带了 `max_tokens` 时，会补发 `message_delta`（`stop_reason: "max_tokens"`，`output_tokens` 记为 `max_tokens`）和 `message_stop`，让客户端看到正常的截断结束。这种请求在日志里没有输入 Token（OpenDesign 只在缺掉的那一帧里给）。

上游错误到 CPA 的映射（流里报的错按错误类型归到同样的状态码）：

| OpenDesign 返回 | 交给 CPA | 效果 |
|---|---|---|
| 401（Key 无效或被吊销） | 401 | 这个 Key 进入冷却 |
| 403（Key 没有权限，比如套餐不含这个模型） | 403 | 这个 Key 进入冷却 |
| 404（模型不存在） | 400 | 只失败这一次请求，不冷却 Key |
| 429（套餐的 5 小时 / 7 天 / 每月额度用完，或限流） | 429，保留 `Retry-After` | 按上游给的时间冷却 |
| 529 | 503 | 可重试 |
| 499 | 408 | 可重试 |

OpenAI 风格的错误信息前面带的 `[code=xxx] ` 会去掉，错误码单独放在 `code` 里。

## 用量

OpenDesign 的额度（钱包余额、套餐 5 小时 / 7 天的剩余额度）只能用账号登录凭据在控制台接口里查，API Key 查不到，所以插件不接 CPA 原生额度面板。

### 登录账号看官方额度

Key 卡片上的「登录账号」按钮走的是官方命令行工具 `vela login` 的设备授权流程，请求内容和 vela 0.1.10 一致（请求体字段、请求头、Go 默认的 User-Agent 都不改）：

1. 插件向 `https://amr-api.open-design.ai/api/v1/cli/device-authorizations` 发起授权，`host` 填运行 CPA 的机器的主机名（Docker 里是容器 ID），会出现在账号的设备列表里；
2. 管理页显示授权链接和验证码，在浏览器里登录账号、核对验证码并确认；
3. 插件按接口要求的间隔轮询，确认后把账号的 `controlKey` 存进这个 Key 的认证文件（字段 `account_control_key` 等），再向接口确认完成。`runtimeKey` 不保存。

之后卡片读取 `GET /api/v1/workspaces/{id}/billing/funding-check`（套餐等级、5 小时 / 7 天窗口的已用 / 上限 / 剩余 credits 和重置时间、余额）和 `GET /api/v1/workspaces/{id}/billing/snapshot`（本期到期时间，读不到不影响额度显示），结果缓存 30 秒，「刷新用量」会强制重新读取。只有账号接口自己返回 401 / 403（JSON 报错）时卡片才提示登录失效，悬停能看到原始原因；Cloudflare 拦截返回的 HTML 页面按网页标题报读取失败，不算登录失效。插件不调用 `/api/v1/billing/summary`：这个接口在 Cloudflare 上拦截 Go 默认的 User-Agent，而 vela 本身发的就是这个 UA。「退出账号」会调用 `/api/v1/cli/logout` 吊销凭据，再从认证文件里删掉这些字段；吊销失败时字段照样删掉，可以到 OpenDesign 网站的设备列表手动移除。

CPA 管理面板「认证文件」页的 OpenDesign 卡片也能看这份额度：插件注册为 CPA 的额度提供方（quota provider），面板点「刷新额度」时插件现读一次，显示套餐（如 `Go (Monthly)`）、余额和 5 小时 / 7 天两条剩余比例和重置时间；没登录账号的 Key 会提示去插件页登录，登录失效时提示重新登录。

注意：账号凭据是账号级的，比 API Key 权限大（能查询账单、发起付款等），和 API Key 一样存在 CPA 的认证文件里。账号凭据只发给 OpenDesign 的账号接口，走这个 Key 的出口代理，管理页拿不到它。

### 请求日志统计

管理页的用量来自插件自己的请求日志：每个 Key 近 5 小时、24 小时、7 天、30 天的 Token（输入 + 输出），鼠标悬停能看到请求数、失败数、缓存 Token 和思考 Token。需要注意：

- 只统计经过本插件的请求，同一个 Key 在别处的调用不算；
- 日志只有 Token，不能换算剩余额度；要看剩余额度请登录账号；
- 日志关闭（`log-retention: 0`）时没有日志统计（登录了账号的卡片照样显示官方额度）；日志保留不足 30 天时，更早的请求不在统计内（鼠标悬停"统计于"能看到日志从什么时候开始）。

## 管理页

在 CPA 管理面板的插件菜单里点 **OpenDesign** 打开，页面地址是：

```text
/v0/resource/plugins/opendesign/manage
```

- 从管理面板里打开时复用面板"记住密码"保存的管理密钥；直接打开时手动输入，只保存在页面内存里。
- **凭据管理** 页：
  - 顶部一条统计条：Key 数（启用的数量），以及全部 Key 近 5 小时 / 7 天 / 30 天的 Token 和请求数。
  - Key 以卡片网格展示（宽屏一行三张，窄屏单列）。每张卡片从上到下依次是：名称（默认是认证文件名，不带 `.json`）、状态、打码后的 Key 和优先级；左边是模型、前缀、出口代理和成功 / 失败次数（2×2），右边是近 5 小时 / 24 小时 / 7 天 / 30 天的 Token（同样 2×2，卡片较窄时移到下方），请求数和明细在鼠标悬停提示里；这四列都按内容宽度排，第一列贴左、最后一列贴右，多出的宽度平均分到列与列之间；登录了 OpenDesign 账号的卡片在这下面多出 5 小时 / 7 天两条剩余额度进度条（剩余不足 30% 变黄、不足 10% 变红，悬停看已用和上限 credits）和一行账号、套餐、余额（悬停看工作区和本期到期时间）；再下面是备注和状态（被限流冷却时这里显示 OpenDesign 的报错）；底部是统计时间和操作按钮（登录账号 / 退出账号、编辑、启停、删除，退出和删除都要点两次确认）。
  - 卡片上可以编辑、启用/停用、删除（删除需要 4 秒内再点一次确认）；可以按名称、Key 或文件名搜索。
  - 打开页面时自动读取所有 Key 的用量（停用的也读），"刷新用量"重新读取全部。用量从本地日志统计，不访问 OpenDesign。
- 添加和编辑在弹窗里进行：名称、出口代理（继承 CPA 全局代理 / 直连 / 代理列表里的代理 / 自定义地址）、模型前缀、优先级，以及"全部对话模型 / 只用选中的模型"，选模型时可以按 ID 或输入类型搜索，并对比上下文长度和单次输出上限；编辑时只列出这个 Key 的套餐里的模型。添加 Key 时默认先用它拉一次模型列表来校验（OpenDesign 暂时连不上时可以勾选"跳过 Key 校验"）；名称留空时用认证文件名。
- **代理** 页：管理出口代理列表。
  - 可以添加、编辑、删除代理。名称留空时用 `主机:端口`；地址只显示隐藏了密码的样子，编辑时地址留空表示不改。
  - 改了某个代理的地址时，正在用它的 Key（`proxy_url` 和旧地址完全相同）会一起改成新地址。删除代理不影响 Key：用它的 Key 保留原地址，变成"自定义地址"。
  - 有 Key 用了列表里没有的代理时，可以点"导入 Key 里的代理"一次加进来。
  - 列表上面固定两行：继承 CPA 全局代理、直连，对应 Key 编辑弹窗里 CPA 内置的两个选项，位置不能改。
  - 列表里的代理可以按住名称左边的手柄拖动排序（鼠标和触屏都行），也可以聚焦手柄后按 ↑ ↓ 移动；顺序存进 `proxies.json`，Key 编辑弹窗的下拉框也按这个顺序列出。
  - 检测要手动点"检测"（单个）或"检测全部"，不会自动检测。检测时插件通过这个代理请求 `https://ipinfo.io/json`，失败再试 `https://api.ip.sb/geoip`，User-Agent 用 `curl/8.7.1`；每次检测新建连接，延迟包含连上代理的时间，单个服务超时 10 秒。
  - 出口列显示国旗和出口 IP，下面是国家和城市（国家名按页面语言显示）；另外两列是延迟和检测时间（本地时间，带日期）。还没检测过的显示"—"。
  - 每次检测的结果（包括失败）都会保存，下次打开页面直接显示上一次的结果。代理改了地址，旧结果作废；CPA 全局代理变了，"继承"那行的旧结果也不再显示。
  - 代理列表和检测结果存在 `<data-dir>/proxies.json`（默认 `plugins/opendesign-data/proxies.json`，权限 0600，里面有代理的用户名密码）。
  - 国旗图标来自 [Twemoji](https://github.com/jdecked/twemoji)（CC-BY 4.0），编进插件里，由 `/v0/resource/plugins/opendesign/flag.svg?cc=<国家代码>` 提供，不访问外部网站。
- **模型列表** 页：所有 Key 的套餐里的模型合在一起，显示默认模型标记、输入类型、成本和能力档位、上下文长度和单次上下文 / 输出上限、有几个 Key 的套餐包含它（悬停看是哪些 Key），以及每个模型在 CPA 里注册的名字和服务它的 Key 数。
- **请求日志** 页：经过本插件的每次调用，最新的在前。
  - 最上面是时间范围：全部、今天、24 小时、近 7 天、近 30 天、自定义（精确到分钟的起止时间，点"应用"生效）。按请求开始时间筛选，选择会记在浏览器里。
  - 统计条：请求数（成功 / 失败）、输入 / 输出 / 缓存 Token、缓存命中率、平均输出速度，按当前时间范围和筛选条件统计。
  - 每行显示时间、模型（和发给 OpenDesign 的上游模型）、思考强度、结果状态码、凭据（认证文件名，不带 .json）、OpenDesign 实际返回的模型、首字时间 / 总耗时、Token 用量；点开一行能看到 CPA 的请求 ID、接口、是否流式、CPA 的 auth_index、打码的 Key、缓存写入、思考 Token、输出速度，失败的还有错误码、出错阶段（输出前 / 流式中途）和错误信息。
  - 可以按请求 ID、模型、凭据、认证文件、错误信息或状态码搜索，按成功 / 失败筛选；每页 20 / 50 / 100 / 200 条，可以输入页码跳转；清空需要 3 秒内再点一次确认。
  - 日志只记录元数据和用量，不记录请求和响应内容。
- **配置概览** 页：插件配置的当前生效值（可以展开成 YAML）、CPA 的强制前缀开关和全局代理。强制前缀没开时会提示同名模型会进入公共号池。
- 支持 English / 中文。

请求日志存在 SQLite 数据库 `<data-dir>/opendesign.db`（默认 `plugins/opendesign-data/opendesign.db`，WAL 模式，旁边会有 `-wal` 和 `-shm` 文件），查询、筛选和统计都在数据库里做。超过 `log-retention` 条或早于 `log-retention-days` 天的记录会自动删掉。SQLite 用的是 [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)，随插件一起编译进去，不依赖系统的 libsqlite3。

详情里显示完整的请求 ID。CPA 日志和请求日志文件名里用的是它的末 8 位，把这 8 位粘贴进搜索框也能找到对应的记录。CPA 只把请求 ID 交给请求拦截器，所以插件注册了一个拦截器：CPA 选中 OpenDesign 凭据后，拦截器把请求 ID 放进内部请求头 `X-OpenDesign-Cpa-Request-Id`，执行器读出来记进日志。这个头不会转发给 OpenDesign；其他渠道的请求，拦截器不做任何修改。拦截器只读请求头和元数据，不接收请求体，需要 CPA 支持插件协议 schema 7；CPA 版本更老时不注册拦截器，日志里也就没有请求 ID。

启用、停用和删除走 CPA 原生的认证文件接口（`PATCH /v0/management/auth-files/status`、`DELETE /v0/management/auth-files?name=...`），和在 CPA 面板里操作效果相同。

## 管理接口

```text
GET   /v0/resource/plugins/opendesign/manage             管理页外壳，无需鉴权，不含数据
GET   /v0/resource/plugins/opendesign/logo.svg           插件 logo，无需鉴权
GET   /v0/resource/plugins/opendesign/flag.svg?cc=hk     国旗图标，无需鉴权
GET   /v0/management/plugins/opendesign/status           插件状态、模型列表摘要、Key 列表
GET   /v0/management/plugins/opendesign/models           所有 Key 的模型（输入类型、上下文、单次上限、档位、哪些 Key 的套餐包含）
POST  /v0/management/plugins/opendesign/models/refresh   重新拉取所有 Key 的模型列表
POST  /v0/management/plugins/opendesign/credentials      添加 Key
PATCH /v0/management/plugins/opendesign/credentials      编辑 Key
GET   /v0/management/plugins/opendesign/credentials/quota?auth_index=...&refresh=1   Key 近 5 小时 / 24 小时 / 7 天 / 30 天的用量（来自请求日志），登录了账号时带上官方额度窗口
POST  /v0/management/plugins/opendesign/account/login    给 Key 登录 OpenDesign 账号 {"auth_index"}，返回授权链接和验证码
GET   /v0/management/plugins/opendesign/account/login?id=...    登录状态：pending / approved / denied / expired / cancelled / failed
DELETE /v0/management/plugins/opendesign/account/login?id=...   取消还在等待的登录
DELETE /v0/management/plugins/opendesign/account?auth_index=... 退出账号并吊销凭据
GET   /v0/management/plugins/opendesign/logs?search=&status=all|success|error&from=&to=&limit=50&offset=0   请求日志（最新的在前）和统计；from / to 是 RFC 3339 时间，按请求开始时间筛选，含 from 不含 to
DELETE /v0/management/plugins/opendesign/logs            清空请求日志
GET   /v0/management/plugins/opendesign/proxies          代理列表（地址隐藏密码、上一次检测结果）和 CPA 全局代理
POST  /v0/management/plugins/opendesign/proxies          添加代理 {"name","url"}
PATCH /v0/management/plugins/opendesign/proxies          修改代理 {"id","name","url"}，改地址时同步改用它的 Key
DELETE /v0/management/plugins/opendesign/proxies?id=...  删除代理，用它的 Key 保留原地址
POST  /v0/management/plugins/opendesign/proxies/import   把 Key 里用到、列表里还没有的代理加进列表
POST  /v0/management/plugins/opendesign/proxies/order    调整代理顺序 {"ids":[...]}
POST  /v0/management/plugins/opendesign/proxies/check    检测出口 IP、国家、城市和延迟并保存结果，{"id"} 或 {"target":"direct"|"inherit"}
```

`/v0/management` 路由由 CPA 宿主先校验管理密钥，通过后才转发给插件。

```bash
# 添加 Key，只注册两个模型，不加前缀
curl -X POST -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"api_key":"<OpenDesign API Key>","models":["deepseek-v4-pro","glm-5.3-flash"],"prefix":""}' \
  http://127.0.0.1:8317/v0/management/plugins/opendesign/credentials

# 改成注册全部对话模型、使用默认前缀、走指定代理
curl -X PATCH -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"auth_index":"<AUTH_INDEX>","models":null,"prefix":null,"proxy_url":"socks5://127.0.0.1:1080"}' \
  http://127.0.0.1:8317/v0/management/plugins/opendesign/credentials
```

添加 Key 时加 `"skip_verify": true` 可以跳过 Key 校验。添加和编辑 Key 时可以用 `"proxy_id"` 代替 `"proxy_url"`，表示使用代理列表里的那个代理（写入它的地址）。Key 的 `proxy_url` 和列表里某个代理完全相同时，`status` 返回的这个 Key 带 `proxy_id` 和 `proxy_name`。

编辑接口只修改请求里出现的字段：`null` 表示恢复默认（`models` 恢复为全部对话模型，`prefix` 恢复为插件默认前缀，`priority` 清除），空字符串表示清除（`label`、`note`、`proxy_url`、`base_url`）。`api_key` 不能修改，换 Key 请新增一个再删掉旧的。

## 安全说明

- 读取和修改 Key 的接口都需要 CPA 管理密钥；公开的只有不含数据的页面外壳、logo 和国旗图标。
- 管理页设置了 `frame-ancestors 'self'`，其他站点不能嵌入。
- 接口和页面只显示打码后的 Key 和隐藏了密码的代理地址；登录账号得到的账号凭据不会返回给页面。
- Key 只发给它自己的 `base-url`（拉模型列表和转发请求），用的是这个 Key 自己的出口。

## 本地构建

管理页在 `web/`，用 React + TypeScript + Vite 写，构建成单个 HTML 文件后嵌进插件。需要 Node 20 和 Go：

```bash
make web        # 构建管理页，输出到 internal/web/dist/index.html
CGO_ENABLED=1 go build -buildmode=c-shared \
  -ldflags '-X main.pluginVersion=0.1.0-dev' \
  -o opendesign.so .
```

macOS 用 `.dylib`，Windows 用 `.dll`。c-shared 构建会顺带生成一个 C 头文件，宿主用不到它。没执行 `make web` 就构建的话，管理页只是一个提示"尚未构建"的占位页。

本地调试管理页可以在 `web/` 里运行 `npm run dev`，开发服务器会把管理接口代理到 `VITE_CPA_BASE`（默认 `http://127.0.0.1:8317`）。

推送 `v*` tag 后，GitHub Actions 会先构建管理页，再跑 gofmt / vet，然后构建五个平台的产物，按插件商店规范打包成 `opendesign_<version>_<goos>_<goarch>.zip` 并连同 `checksums.txt` 发布 Release。CI 同时跑 Go 和管理页的单元测试。

## License

MIT
