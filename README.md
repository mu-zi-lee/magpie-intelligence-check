# Magpie Intelligence Check

Magpie 插件，用于对指定的主力模型做后台质量检测。默认“检测＋告警”：检测过期、检测中或检测服务不可用都不阻断业务请求；可对固定上游显式开启严格模式，在自动题复测仍异常时拦截。

项目由两部分组成：

- **Magpie 插件**：`plugin/index.mjs` 负责启动后台、注册控制台入口和读取 Magpie 配置；`plugin/gate.middleware.js` 是 Magpie 请求门禁。
- **Go 后台**：`cmd/quality-check` 提供本机管理 API、SQLite 状态存储、定时复检、可选日志监听、模型探针和临时远程控制台。管理页面嵌入在 Go 二进制中。

插件只对已启用目标的人工暂停或显式严格策略进行拦截。它不会替换 Magpie 的供应商配置、API 地址或 Key，也不会自动切换模型。

## 工作流程

```text
客户端 -> Magpie -> quality-check 门禁 -> 上游模型
                         |
                         +-- 未启用监测 ----------------> 直接通过
                         +-- 默认检测＋告警 ------------> 直接通过
                         +-- 严格模式且质量复测失败 ----> 返回 403
                         +-- 人工暂停 ------------------> 返回 403

Go 后台定时扫描已启用目标 -> 预算检查与并发去重 -> 独立探针 -> 保存质量结论与告警
```

首次使用、检测结果超过 15 分钟、检测中、检测超时和预算耗尽都不会单独导致 `403`。默认模式即使发现质量异常也只告警，业务请求仍交给上游；上游自身的错误仍会正常返回。

后台每秒扫描一次，自动检测结论每 15 分钟刷新。同一模型的并发触发合并为一轮检测，沿用每日预算和失败冷却。已启用目标在闲置期间也会检测并消耗预算，不需要时请关闭该目标的监测。检测运行状态与上次质量结论分别保存，检测失败不会抹去已有结论或解除人工暂停。

升级后的已有目标默认使用“检测＋告警”，保留历史记录和人工暂停。严格模式需在控制台对目标明确选择；严格模式也不因结果过期而拦截，但会保留已确认的质量异常，直到后续探针通过、切回告警模式或限时放行。

## 安装

### 从 Magpie 的安装框添加

在“已有想装的？”输入框填写：

```text
github:mu-zi-lee/magpie-intelligence-check
```

仓库包含五个平台的 Go 二进制，插件会按 Magpie 所在机器的系统和架构选择。
供应商显示名为 **Intelligence Check**。更新后请重新安装插件并重新打开管理入口。

### 使用发行包

发行包在 `dist/` 中，按平台提供完整插件目录和已编译的 Go 二进制：

| 平台 | 文件 |
| --- | --- |
| Linux x86_64 | `magpie-intelligence-check-linux-amd64.tgz` |
| Linux ARM64 | `magpie-intelligence-check-linux-arm64.tgz` |
| macOS Intel | `magpie-intelligence-check-darwin-amd64.tgz` |
| macOS Apple Silicon | `magpie-intelligence-check-darwin-arm64.tgz` |
| Windows x86_64 | `magpie-intelligence-check-windows-amd64.tgz` |

解压后，把解压出的**完整目录**添加到 Magpie：

```sh
tar -xzf magpie-intelligence-check-darwin-arm64.tgz
magpie plugin add /absolute/path/to/magpie-intelligence-check-darwin-arm64
```

安装目录需要保持固定并可写。插件会在其中读取 JavaScript 门禁文件，并由 Go 后台原子更新该文件。

发行包已经包含对应平台的 Go 二进制，运行机器不需要安装 Go 或 Node.js。

### 从源码构建

需要 Go `1.26` 工具链：

```sh
go build -o bin/quality-check ./cmd/quality-check
magpie plugin add /absolute/path/to/magpie-intelligence-check
```

开发目录需要包含 `plugin/`、`bin/quality-check`、`package.json` 和 `third_party/`。Magpie 主进程（桌面应用、`magpie web` 或 `magpie serve`）必须保持运行；插件后台跟随宿主进程生命周期。

## 首次配置

1. 在 Magpie 插件入口打开“本机质量检测控制台”。
2. 点击右上角设置图标，在“连接与预算”中确认网关 URL、Key 和预算；日志路径可选，保存后会验证连接。
3. 点击“刷新模型”，选择完整模型 ID，例如 `provider/model`，添加监测目标。
4. 等待后台“牛奶推理”检测，或手动重新检测，确认网关和探针可用。
5. 从客户端发送一次该模型的业务请求，确认默认模式正常转发；需要异常拦截时再选择严格模式。

插件会优先通过以下方式发现本机网关：

- `MAGPIE_ADDR` 环境变量；
- `$XDG_CONFIG_HOME/magpie/settings.json` 中的监听端口；
- 默认地址 `http://127.0.0.1:3425`。

发现通过回环地址访问网关，并用 Key `magpie` 请求 `/v1/models` 验证连接。`0.0.0.0:端口`、`[::]:端口` 和 `:端口` 会转换为对应的本机地址。启动时在后台发现，不阻塞 Magpie 初始化；网关尚未就绪时每 5 秒重试。远程地址或其他 Key 需要在控制台手动填写。已有配置不会被自动覆盖。

如果仍提示“请配置 Magpie 网关 URL 和 Key”，点击连接设置，填写 **Magpie 所在机器**可访问的网关地址，例如 `http://127.0.0.1:3425`，以及网关 Key（本机默认 `magpie`），再保存。它不是 Cloudflare 控制台域名，也不是上游供应商地址。Docker 中网关不在同一容器时，请填写容器可访问的实际地址与 Key。

控制台提供模型监测、质量题库、模型指纹和检测记录四个视图；支持暗亮主题、手机操作、记录筛选、历史指纹批次和原始回答下载。自动刷新会保留未保存的设置与已展开的回答。

只需要填写 Magpie 网关的 URL 和 Key，不需要重复填写各上游供应商凭据。

### 日志源

自动检测由后台定时任务驱动，不依赖 Magpie 日志。可选日志监听会读取门禁输出的触发标记，与定时任务共用同一去重和预算检查。检测异常在控制台显示，并以 `MAGPIE_QUALITY_WARNING` 写入宿主日志，不包含对话正文或网关 Key。

例如：

```sh
mkdir -p "$HOME/.local/state/magpie"
chmod 700 "$HOME/.local/state/magpie"
umask 077
magpie serve 2>> "$HOME/.local/state/magpie/gateway.log"
```

然后把这个**绝对路径**填入控制台。已有 systemd 或其他服务可以直接把标准错误重定向到可读文件，不需要启动第二个 Magpie 实例。

后台每秒检查日志，并支持文件截断、重命名和轮转。首次打开日志时从文件末尾开始，不会补跑历史内容。

没有可读日志时，后台定时检测和手动检测仍可用。日志文件可能包含其他 Magpie 敏感信息，请限制其权限。

## 远程控制台

选择“打开临时远程控制台（Cloudflare）”后，插件会启动 Cloudflare Quick Tunnel，把本机管理页面临时暴露为 HTTPS 地址：

- 一次性 `/open/...` 链接有效 10 分钟；
- 浏览器会话最长 30 分钟；
- 临时通道最长 30 分钟，也可以在页面中提前结束；
- 关闭通道会撤销远程会话，但不会解除模型暂停，也不会停止检测后台；
- 只开放管理页面，不开放 Magpie 模型 API，网关 Key 不返回浏览器。

插件优先使用 PATH 中已有的官方 `cloudflared`。找不到时，默认下载并校验固定版本 `2026.10.0`，缓存到数据目录的 `runtime/`，不需要 root，也不会修改系统 PATH。自动下载需要访问 GitHub 发行文件和 Cloudflare 隧道服务；可通过 `cloudflaredPath` 指定已有客户端，或关闭自动下载。

远程入口会先等待公网 `/health` 就绪，最长 60 秒，再返回管理链接。
如果出现 `502`，本轮就绪检查失败会关闭通道；检查网络后从插件重新打开。
管理链接从外部页面打开时，使用 `HttpOnly`、`SameSite=Lax` 会话 Cookie 完成重定向；
会话过期后需要从插件取得新链接。

不要只打开临时域名首页，必须使用插件返回的完整 `/open/...` 链接。不要把管理端口直接绑定到公网。

## 检测规则

内置 7 道题：

| ID | 题目 | 是否参与自动门禁 |
| --- | --- | --- |
| `milk` | 牛奶推理 | 是 |
| `repeat` | 逐字复述 | 否，人工评价 |
| `japan` | 日本首相 | 否，人工评价 |
| `person` | 人物知识 | 否，人工评价 |
| `html` | HTML/SVG 动画 | 否，人工评价 |
| `svg` | SVG 创作 | 否，人工评价 |
| `map` | 世界海陆地图 | 否，人工评价 |

提示词正文按源码原样发送，保留字符、标点、空格和空行。题目文本有 SHA-256 回归测试；代码围栏、语言标记和评价说明不会追加到发送正文。

自动门禁只使用 `milk`：最终答案为 `46.24`，或最后一个带 `inches`/`inch`/`英寸` 单位的数字为 `46.24` 时通过。首次答案异常会独立复测一次；两次都未通过则进入“需要人工确认”。

其他题目会保存原始回答和 token 用量，在质量题库页面直接展示，也可选择历史回答、复制或下载。失败与截断会显示原因，并保留已返回的回答正文。用户可保存“待确认 / 确认通过 / 标记问题”与审核备注；结论记录在对应回答上，不更改自动评分或模型门禁。自动辅助检查展示字符数、逐字复述覆盖率或代码标记等可验证线索，不识别模型身份，也不替代人工审核。后台不会执行模型生成的 HTML、SVG 或 Python。

### 预算与超时

- 默认每个模型每天最多 120 次探针请求，可在控制台设置为 1–10000；
- 每轮自动检测最多调用 2 次；
- 单次请求 `max_tokens` 为 8192；
- 单轮总超时为 180 秒；
- 网关调用失败、超时、空回答、截断、无效响应或预算耗尽会标记为“检测不可用”；
- 检测不可用后自动冷却 15 分钟，手动检测可以立即发起。

探针请求使用 Magpie 的 Chat Completions 接口，并把模型改写为内部别名 `quality-probe/<随机别名>/<完整模型 ID>`。门禁会放行这个内部别名，避免探针再次触发门禁。

这些题目只是行为与能力线索，不是模型身份认证、供应商认证或通用智商排名。单题失败不能证明模型被替换；知识题有时效性，复述指纹也可能随版本变化。

### 题库反馈与更新

质量测试支持流式正文（网关返回普通 JSON 时仍兼容）、等待时间和取消；取消或连接中断保留已返回正文，生成中不会执行预览。HTML 可切换代码块、选择手机/平板/桌面宽度、页内放大，并可选择明确标注的 HTML + CSS/JS 组合结果；原始回答不修改。离开题库会停止预览。历史支持题目、模型、结果、审核状态筛选，双回答对比与筛选结果 JSON 导出。清理需确认，检测期间不可清理；只移除回答与审核，保留已消耗预算计数，不更改模型门禁。

HTML 预览额外移除声明式自动刷新、外链导航、表单目标和 base 标签，并使用不含同源、顶层导航或弹窗权限的沙箱。允许任意内联脚本时，浏览器不能可靠禁止 iframe 自身的脚本导航，因而这不是完整的离线执行环境；只预览可信回答，不要在预览中输入敏感信息。网络 API、外部资源和管理页访问限制仍生效。

SVG 题中的完整 SVG 可直接静态预览，不执行脚本、外部资源或动画。HTML 题可在隔离的 iframe 中实时预览模型生成的页面、内联 JavaScript、Canvas 和动画，并可停止或重新加载；轮询不会重启动画。预览不开放管理页面、Cookie 或存储访问权限，禁止网络 API 请求、外部脚本、嵌套页面、表单提交和弹窗。依赖 CDN 或外部图片的回答可能无法完整显示；不可信脚本仍可能消耗浏览器资源，必要时停止预览或关闭页面。原始回答保持不变，可查看、复制与下载。关闭模型监测只关闭门禁与自动触发，仍可手动运行质量题和指纹挑战；删除目标后测试选择器同步移除，再添加后同步更新。

欢迎实际测试后反馈：哪些题目现在仍然好用，哪些已经失效、答案有争议，或容易误判？现有题库不保证对所有模型版本、渠道和系统提示词都有效，请结合页面中的原始回答自行确认。

如果你有**更新、可用的测试题**，或发现更适合推理、翻译、知识、复述等能力的题目，请[提交题库反馈 Issue](https://github.com/mu-zi-lee/magpie-intelligence-check/issues/new?template=question-bank.yml)。维护者会审核后手动添加或更新，不会自动把社区题目纳入门禁。

提交时请提供：

- 题目原文与来源；原创题目请注明。
- 测试的模型、版本或日期、渠道，以及是否使用额外系统提示词。
- 原始回答、预期答案或人工评价标准。
- 为什么认为它有效；有多次测试或不同模型的对照结果更好。
- 已知限制，例如知识时效性、不同合理答案或可能的误判。

请勿提交 API Key、管理链接、私人对话或其他敏感信息。无需为反馈专门重复付费测试，已有的匿名化结果即可。

### ModelTrace 模型指纹

控制台提供独立的 **ModelTrace 模型指纹** 检测。选择已启用监测的模型，点击“运行三条指纹挑战”，后台通过同一 Magpie 网关发送三次独立数字选择请求。每条要求生成 292–332 个 1–355 范围内的整数。

- 每轮预留 3 次当日预算，不足时不发请求；没有自动复测；
- 每条请求最多 180 秒，三条顺序执行；关闭浏览器不会中断后台采集；
- 保存每条挑战原文、原始回答、token 用量和批次，刷新页面后仍可查看；
- 三条回答都有效才展示归因；拒答、截断、调用失败或有效数字不足不会被当作模型异常；
- 浏览器本地使用 ModelTrace 原版评分算法及固定候选库，展示候选模型排名和家族概率；
- 不改变牛奶题的通过状态、人工暂停或临时放行。

当前候选库含 GPT、Claude 两个家族的 18 个模型。概率是库内候选的闭集概率：
即使真实模型不在库里，也会归到某个最相似候选，因此不能据此认证身份或自动封锁模型。
渠道系统提示词和协议转换也可能改变输出分布。

来源为 `xqy2006/ModelTrace`，固定提交和资源校验值见 `third_party/modeltrace/README.md`；
采用 MIT 许可，评分代码与候选库嵌入 Go 二进制，无需 Python、外部评分服务或运行时下载。

## 状态与人工操作

控制台中的目标状态包括：

- **未检测**：目标已启用，但没有有效检测结果；
- **检测中**：后台正在运行自动检测；
- **通过 / 待复检**：上次检测通过，过期后后台刷新，业务请求继续转发；
- **质量异常**：自动题复测仍异常，默认只告警，严格模式才拦截；
- **检测不可用**：网关、超时或预算等原因导致本轮无法完成，保留上次质量结论；
- **人工暂停**：独立于检测结论，直到用户解除暂停；检测通过不会覆盖人工暂停；
- **监测关闭**：目标不再受门禁控制。

可执行操作：

| 操作 | 作用 |
| --- | --- |
| 重新检测 | 立即运行所选题目，计入预算 |
| 检测＋告警 | 默认策略，质量异常只记录与告警 |
| 严格模式 | 仅固定 provider/model 可选，自动题复测失败时拦截 |
| 放行 15 分钟 | 暂时允许该目标请求，保留异常记录 |
| 暂停 / 解除暂停 | 独立设置或解除人工暂停，不更改检测结论 |
| 关闭监测 | 关闭该目标的检测和门禁 |
| 删除监测目标 | 从列表移除并解除门禁，保留历史回答和审核；检测中不可删除 |

严格模式的质量异常或人工暂停返回实际 API `403`，消息包含目标、原因和事件编号，例如：

```text
[MAGPIE_QUALITY_REVIEW_REQUIRED] 严格模式：质量检测复测未通过。
本次请求未发往上游。
```

插件只保证匹配目标的模型请求被拦截，不控制客户端已经开始的本地工具操作或整个任务流程。

## 插件选项

可以在 Magpie 插件配置中传入：

```json
{
  "dataDir": "/srv/magpie-quality/data",
  "gatewayURL": "http://127.0.0.1:3425",
  "gatewayKey": "magpie",
  "logPath": "/srv/magpie/gateway.log",
  "binaryPath": "/srv/magpie-quality/bin/quality-check",
  "cloudflaredPath": "/usr/local/bin/cloudflared",
  "autoInstallCloudflared": true
}
```

字段说明：

- `dataDir`：SQLite、门禁状态和 Cloudflare 缓存目录。默认 `~/.config/magpie-quality-check`；
- `gatewayURL`、`gatewayKey`、`logPath`：用于首次自动连接配置，不覆盖控制台已有设置；
- `binaryPath`：指定 Go 后台二进制；
- `cloudflaredPath`：指定官方 `cloudflared`；
- `autoInstallCloudflared`：设为 `false` 时禁止自动下载 Cloudflare 客户端。

数据目录会以私有权限创建，SQLite 中保存网关凭据、历史结果和目标状态。停用、退出或重启不会清除数据；卸载插件也不会自动删除数据目录，请按需手动备份或清理。

同一个 `dataDir` 只允许一个检测后台运行。多个 Magpie 实例必须使用独立的插件目录和 `dataDir`。

## 限制与兼容性

- 依赖 Magpie 的插件系统和中间件热加载；日志源可选；
- 门禁按请求中的完整模型 ID 判断；`group/*` 路由组仅支持检测与告警，严格模式必须选择固定 `provider/model`，一次组级采样不能代表组内所有上游；
- 由于门禁文件热加载存在传播窗口，Magpie 中间件加载失败时默认放行；这不是强安全隔离；
- 网关 Key 需要有权限调用 `/v1/models` 和 `/v1/chat/completions`，并允许探针模型别名；
- 当前探针使用非流式 Chat Completions，由 Magpie 负责上游协议转换；
- 发行脚本生成 Linux、macOS 和 Windows 的交叉编译包，实际部署仍需检查网络、文件权限、日志路径和 Magpie 版本；
- Cloudflare Quick Tunnel 是临时通道，不保证公网可用性或稳定性。

## 开发与验收

运行 Go 测试和插件测试：

```sh
go test -race ./...
go vet ./...
node --test plugin/*.test.mjs
```

构建发行包和校验清单：

```sh
node scripts/build-release.mjs
node scripts/package-check.mjs
```

可选的集成检查：

```sh
MAGPIE_BINARY=/absolute/path/to/magpie node scripts/magpie-integration.mjs
MAGPIE_BINARY=/absolute/path/to/magpie MAGPIE_BUN=/absolute/path/to/bun node scripts/gateway-integration.mjs
MAGPIE_BINARY=/absolute/path/to/magpie node scripts/install-check.mjs
node scripts/plugin-check.mjs
node scripts/client-check.mjs
node scripts/cloudflare-check.mjs
node scripts/browser-check.mjs
node scripts/auth-browser-check.mjs
```

其中：

- `magpie-integration.mjs` 验证真实 Magpie 网关中的默认转发、无日志后台探针、显式严格模式及人工暂停；
- `gateway-integration.mjs` 验证真实 Magpie 管理页面加载完整插件后自动发现、验证并保存网关；需要 Node.js 24，`MAGPIE_BUN` 指向已有 Bun，测试使用独立的临时配置；
- `install-check.mjs` 验证发行包解压、插件注册和宿主退出清理；
- `client-check.mjs` 验证客户端能看到 `MAGPIE_QUALITY_REVIEW_REQUIRED` API 错误；
- `cloudflare-check.mjs` 验证临时 HTTPS 入口、会话交换和关闭；
- `browser-check.mjs` 需要 Playwright，可通过 `PLAYWRIGHT_MODULE` 指定模块位置。
- `auth-browser-check.mjs` 使用真实浏览器验证从外部页面打开管理链接、票据重定向和会话刷新。

## 目录结构

```text
cmd/quality-check/       Go 后台入口
internal/check/          状态、探针、HTTP 控制台和日志监听
internal/check/web/      嵌入 Go 二进制的管理页面
plugin/index.mjs         Magpie 插件入口
plugin/gate.middleware.js 请求门禁（运行时由后台原子更新）
plugin/cloudflared-runtime.mjs  Cloudflare 客户端发现、下载和校验
scripts/                 构建、集成和客户端验收脚本
third_party/cloudflared/ Cloudflare 客户端许可文件
```

## 许可

本项目采用 MIT License。Cloudflare 客户端相关许可随包保留在 `third_party/cloudflared/LICENSE`；ModelTrace 许可保留在 `third_party/modeltrace/LICENSE`。
页面内置 Space Grotesk、Space Mono 字体和 Lucide 图标，无需运行时 CDN；许可分别保留在 `third_party/fonts/` 和 `third_party/lucide/LICENSE`。
