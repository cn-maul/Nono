# Nono 开发计划

Windows 顶部刘海式 Agent 状态条。Go + mygo（WebView2），首期接入本机 Qoder。

## 1. 产品目标

- 紧贴屏幕顶部，类似手机刘海，平时收起，点击展开。
- 任一时刻只显示一个会话，占满整个刘海区域。
- 左上角显示 agent logo，用于区分不同 agent 工具。
- 左右点击切换会话。
- 当会话状态变化（任务结束、失败停止、重连失败、等待回复、权限申请）时，自动切换到该会话。
- 多个会话先后或同时变化时进入通知队列，FIFO 显示；点击处理后自动切到下一个。
- 展示指标：
  - 当前会话状态
  - 最近 5 轮平均首字时长
  - 首字后整体时长
  - token 速度

## 2. 已确认决策

| 项目 | 决策 |
|---|---|
| 渲染方式 | WebView（HTML/CSS/TypeScript），走 mygo |
| 展开行为 | 仅点击切换，不做鼠标移出自动收起 |
| 指标范围 | 当前会话最近 5 轮 |
| 会话展示 | 单会话占满刘海，左上角 agent logo，左右切换 |
| 通知行为 | 状态变化自动切换；按 FIFO 队列逐个显示 |
| Qoder 数据 | 只读，不修改 Qoder 配置、不安装 hook |

## 3. 运行环境（本机已验证）

- Go 1.27.3 windows/amd64
- WebView2 Runtime 156.0.4314.58（Windows 11 自带）
- mygo 最新版本 v0.7.4
- mygo 无 cgo
- SQLite 驱动使用 `modernc.org/sqlite`（纯 Go）

## 4. 总体架构

```
┌────────────────────────────────────────────┐
│ mygo 窗口                                   │
│  WebView2 + HTML/CSS/TS                     │
│  透明 / 无边框 / 置顶 / 不占任务栏           │
└───────────────▲────────────────────────────┘
                │ Snapshot / UIState JSON
                │ mygo binding + 事件推送
┌───────────────┴────────────────────────────┐
│ Go 采集层                                   │
│  ├─ process   检测 Qoder CN.exe             │
│  ├─ logtail   增量 tail main/renderer log   │
│  ├─ segment   增量 tail segments/*.jsonl    │
│  ├─ sqlite    只读查询 main.sqlite / buffer │
│  ├─ status    状态机 + 边沿检测             │
│  ├─ metrics   首字 / 时长 / token 速度      │
│  └─ notice    通知队列                      │
└────────────────────────────────────────────┘
```

采集层轮询间隔 300–500ms，按文件 offset 增量读取，不做整库重查。

## 5. 本机数据源（实测确认）

### 5.1 路径

Qoder 桌面数据：

- `%APPDATA%\com.qodercn.app.stable\main.sqlite`
- `%APPDATA%\com.qodercn.app.stable\chat-session-turn-payload-buffer.sqlite`
- `%APPDATA%\com.qodercn.app.stable\logs\<启动ID>\main.log`
- `%APPDATA%\com.qodercn.app.stable\logs\<启动ID>\renderer.log`

Qoder 用户目录：

- `%USERPROFILE%\.qoder-cn\projects\<项目slug>\<session-id>.jsonl`
- `%USERPROFILE%\.qoder-cn\logs\sessions\<项目slug>\<session-id>\segments\<run>.jsonl`
- `%USERPROFILE%\.qoder-cn\logs\runs\<run>\qodercli.log`

安装目录（版本号会变）：

- `%LOCALAPPDATA%\Programs\Qoder CN\.qoder-versions\<version>\resources\app-icon.ico`

### 5.2 字段与用途

状态日志（main.log / renderer.log）：

```
ChatSession control state committed/applied
{
  sessionId, previousRuntimeState, runtimeState,
  activeTurnId, activeTurnState,
  pendingInteractionCount, queuedTurnCount,
  terminalTurnId, terminalStatus
}
```

已观察到的取值：

- runtimeState: cold / opening / ready / running / waiting-user / interrupted / error
- activeTurnState: starting / running / waiting-user / completed / failed / interrupted
- terminalStatus: completed / failed / interrupted

Segment 日志（每会话独立，事件带 `ts`、`turn_id`、`loop_id`）：

- `input.prompt.received`
- `input.prompt.submitted`
- `turn.started`
- `loop.iteration.started`
- `model.request.started`
- `model.response.completed`，data 含 `input_tokens`、`output_tokens`、`cache_read_input_tokens`
- `tool.requested`
- `tool.shell.started` / `tool.shell.finished`
- `permission.resolved`
- `turn.finished`，含 `reason`、`duration_ms`、tokens
- `session.phase.started` / `session.phase.finished`

项目 JSONL：

- assistant 记录的 `message.usage` 含 `input_tokens`、`output_tokens`、`cache_read_input_tokens`。
- 记录 `timestamp` 为消息完成时间。

SQLite：

- `chat_sessions`：session_id、title、cwd、model、updated_at、archived、session_kind
- `chat_session_messages`：payload_json 内 parts 含 `startedAt`、`completedAt`、`durationMs`；另有 `turn_started_at`、`turnMetrics`
- `chat_session_turn_states`：status（starting / running / waiting-user / completed / failed / canceled / interrupted），当前版本可能为空，作为兜底
- `chat-session-turn-payload-buffer.sqlite` 的 `active_turn_projection_items`：live turn 的 `turn_started_at`、item_type、created_at
- `local_inbox_items`：event_type（user_input_required / execution_failed / updated）、handling_status、occurred_at

## 6. 状态模型

| 界面状态 | 判定 |
|---|---|
| 未运行 | 无 `Qoder CN.exe` 进程 |
| 空闲 | 进程在，无 active turn |
| 启动中 | runtimeState=opening / activeTurnState=starting |
| 思考/生成 | running，最近事件为 model.request.started |
| 执行工具 | running，最近事件为 tool.requested / tool.shell.started |
| 等待回复 | waiting-user，pendingInteractionCount=0 |
| 权限申请 | waiting-user，pendingInteractionCount>0，或命中 permission 事件 |
| 队列中 | queuedTurnCount>0 |
| 重连中 | SSE reconnect scheduled / 连接失败重试 |
| 重连失败停止 | runtimeState=error，或重连后 terminalStatus=failed |
| 失败停止 | terminalStatus=failed |
| 已中断 | interrupted |
| 已完成 | terminalStatus=completed |

状态来源优先级：

1. 最新的 control state 日志（按 sessionId 分别维护）
2. 最新的 segment 事件
3. SQLite 兜底

## 7. 指标口径

### 7.1 一轮的时间锚点

- 本轮开始：`turn.started` 的 ts（缺失时用 `input.prompt.submitted`）
- 首字：本轮最早输出片段的 `startedAt`
  - 优先取 `chat_session_messages.payload_json` 中 parts 的 `startedAt`
  - 无 thinking 时该字段可能缺失，降级为首个 assistant 落盘时间，UI 标注 `≈`
- 本轮结束：`turn.finished` 的 ts，或终端 control state

### 7.2 计算

```
首字时长 TTFT = 首字 - 本轮开始
首字后时长     = 本轮结束 - 首字
单轮 token 速度 = Σ output_tokens / Σ 单次请求耗时
单次请求耗时    = model.response.completed.ts - model.request.started.ts
平均首字        = 当前会话最近 5 轮 TTFT 的算术平均
```

说明：

- token 速度按请求耗时计算，包含该请求的 TTFT，属于偏保守口径。
- 流式进行中，首字和 token 速度只能等当前响应完成后再更新；进行中时长用秒表实时推进。
- 一轮包含多次模型请求时，token 速度取加权平均，而不是简单平均。

## 8. 会话模型与切换

### 8.1 数据结构

```go
type Session struct {
    AgentID      string // 首期固定 qoder
    AgentName    string // Qoder
    AgentLogo    string // 资源路径
    ID           string
    Project      string // cwd 目录名
    Title        string
    Model        string
    State        Status
    Pending      int
    Queued       int
    Metrics      Metrics // 最近 5 轮
    LastActivity time.Time
}

type Metrics struct {
    AvgTTFT       time.Duration
    TurnDuration  time.Duration
    TokensPerSec  float64
    RecentTurns   []TurnMetric
}

type UIState struct {
    Sessions      []Session
    FocusedID     string
    Notices       []Notice
    Browsing      bool
    PreviousFocus string
}
```

### 8.2 会话集合

- 只保留 `session_kind=standard`、未归档、未删除的会话。
- 排除 sidechain、subagent、automation 会话。
- 保留运行中、等待中，或最近 30 分钟有更新的会话。
- 最多 5 个。

### 8.3 切换规则

- 初始顺序：需要处理（权限申请 / 等待回复）> 运行中 > 最近活动。
- 用户手动左右切换后，保持当前选择，不因后台状态变化跳走。
- 到头环绕。
- 只有 1 个会话时隐藏左右箭头。

## 9. 通知队列

### 9.1 通知事件

| 事件 | 触发条件 | 显示 |
|---|---|---|
| 任务结束 | running → completed | 已完成 |
| 失败停止 | terminalStatus=failed | 失败停止 |
| 重连失败停止 | runtimeState=error，或重连后失败 | 重连失败停止 |
| 等待回复 | waiting-user 且无权限事件 | 等待你回复 |
| 权限申请 | waiting-user + pendingInteractionCount>0 | 权限申请 |
| 已中断 | interrupted | 已中断（可选） |

只在状态跳变时入队。启动时先建立基线，历史事件不回放。

### 9.2 队列规则

- FIFO：按状态改变时间排序；同时间按 sessionId 稳定排序。
- 去重：同一会话同一时刻只保留一项；排队期间再次变化时更新内容，保持原位置。
- 自动切换：事件到达时，若该会话不是当前显示会话，立即切过去。
- 自动消失：排队会话在别处被处理（状态离开待处理）时，自动出队并跳到下一个。
- 出队：点击「去处理」或「跳过」后出队，切到下一个；队列空时恢复到通知前的会话。
- 未处理标记：出队不等于已处理；状态仍未解除的会话，在对应方向箭头上保留琥珀点。
- 上限：最多 10 项，超出丢弃最旧的并在展开态提示。
- 出队前重新校验状态，已解除的项直接跳过。

### 9.3 浏览与通知的关系

- 手动左右切换 = 浏览模式，不消耗通知队列。
- 收到新通知 = 进入通知模式，显示队首。
- 队列清空后，若通知前的会话仍存在则恢复，否则回到最近活动会话。

## 10. 窗口与交互

### 10.1 窗口参数

- 收起态：320 × 39（39 是 Windows 对该窗口样式的最小高度）
- 展开态：560 × 224
- 位置：主屏顶部居中，y = 0
- `Frameless: true`
- `Transparent: true`
- `AlwaysOnTop: true`
- `SkipTaskbar: true`
- 禁止调整大小；不设置 `DisableMove`
- 不使用 `Panel`（会触发 DWM 四角圆角，顶部无法贴边）
- Win32 设置 `WS_EX_NOACTIVATE | WS_EX_TOOLWINDOW`：点击不抢焦点、不进 Alt+Tab
- 按显示器 DPI 处理尺寸

### 10.2 交互

- 左边缘 28px：上一个会话
- 右边缘 28px：下一个会话
- 中间：展开 / 收起
- 展开态左右边缘仍为切换
- 切换动画 150ms 横向滑入

### 10.3 收起态

```
╭──────────────────────────────────────────╮
│ [Q] ● Nono  生成中  首字 2.1s · 45 t/s ‹ ›│
╰──────────────────────────────────────────╯
```

通知模式：

```
╭──────────────────────────────────────────╮
│ [Q] ● Nono  权限申请 · 等待确认    ‹  ›  │
│                          待处理 1 / 2    │
╰──────────────────────────────────────────╯
```

- 通知到达时默认不自动展开，只切换内容并做一次状态色脉冲。
- 状态色：运行绿、等待蓝、权限琥珀、失败红、未运行灰。

### 10.4 展开态

```
╭──────────────────────────────────────────╮
│ [Q] Qoder            2 / 3        ‹    › │
│ Nono · 规划刘海屏 Agent 监控              │
│ ● 权限申请 · 等待你确认                   │
│                                          │
│ 首字均值 1.8s   首字后 42s   tok/s 38     │
│ 最近 5 轮  ▁▂▃▅▂                          │
│                                          │
│           [ 去处理 ]   [ 跳过 ]           │
╰──────────────────────────────────────────╯
```

- 「去处理」：把 Qoder 窗口恢复并置前，出队，切到下一个通知。
- 「跳过」：不操作 Qoder，直接出队。

### 10.5 技术限制

Qoder 未注册 URL 协议（`qoder://` 存在于包内但系统未注册），无法从外部直接定位某个会话。因此「去处理」只能：

1. 用 Win32 将 Qoder 窗口恢复并置前；
2. 在刘海屏显示项目名和会话标题，供人工定位。

采集层预留 `FocusSession(sessionID)` 接口，若 Qoder 后续支持深链再接入。

## 11. 数据采集实现

- 进程检测：`Qoder CN.exe` 是否存在。
- 日志：定位最新的 `logs/<启动ID>/`，记录 offset 增量读取；启动目录变化时重置 offset。
- Segment：按 sessionId 定位 `logs/sessions/*/<session-id>/segments/`，增量读取最新 run 文件。
- SQLite：`modernc.org/sqlite`，只读打开，设置 `busy_timeout`；查询失败时跳过本轮，不抛到 UI。
- 通知队列与状态机为纯 Go，可独立单测。
- 单实例锁，避免启动多个 Nono。
- 配置文件 `config.json`：
  - 轮询间隔
  - 指标轮数（默认 5）
  - 展开 / 收起尺寸
  - 是否开机自启
  - 通知上限

## 12. 目录结构

```
Nono/
  main.go
  config.json
  internal/
    notch/          # 窗口、位置、点击热区、推送
    agent/          # Adapter 接口与注册表
    qoder/          # Qoder 适配器
      locator.go
      tailer.go
      sqlite.go
      status.go
      metrics.go
      notice.go
    ui/             # UIState 组装
  web/
    index.html
    src/
    assets/         # Qoder logo 等
  PLAN.md
```

Adapter 接口（为多 agent 预留）：

```go
type Adapter interface {
    ID() string
    Name() string
    Logo() []byte
    Collect(ctx context.Context) ([]Session, error)
    FocusSession(sessionID string) error
}
```

首期只实现 Qoder。

## 13. 里程碑

### M0 · 窗口壳（0.5 天）

- mygo 创建顶部居中、透明、无边框、置顶、不占任务栏的窗口。
- 验证 WebView2 透明背景、DPI、多屏定位。
- 验证点击不抢 Qoder 焦点。
- 交付：可点击展开/收起的空壳。

### M1 · 采集器与通知队列（1.5 天）

- 实现进程检测、日志增量读取、SQLite 只读查询。
- 实现状态机、指标计算、通知队列、基线初始化。
- 提供 `nono probe` 与 `nono probe --watch`，输出 JSON 与队列事件。
- 验收：双开会话场景数据正确。

### M2 · UI 对接（1.5 天）

- 收起态 / 展开态、agent logo、左右切换、通知角标。
- 自动切换、去处理 / 跳过、恢复通知前会话。
- 验收：通知队列场景全部通过。

### M3 · 打磨（1 天）

- 动画、托盘、开机自启、配置。
- Qoder 未启动、日志缺失、DB 锁、版本升级字段缺失的兜底。
- Win32 前置 Qoder 窗口。

## 14. 验收用例

1. 两个项目各开一个会话，A 运行、B 等待授权：收起态显示 B 并脉冲；展开能看到 B 的详情。
2. A 先结束、B 后申请权限：先显示 A，角标 `1/2`；去处理后显示 B。
3. 同一会话在排队期间再次变化：队列仍只有一项，内容更新，位置不变。
4. 在 Qoder 里手动处理掉 B：B 自动出队并跳到下一个。
5. 启动 Nono 时已有历史完成记录：不产生任何通知。
6. 只有 1 个会话：左右箭头隐藏。
7. Qoder 未启动：显示「未运行」，不产生通知。
8. 两个会话指标互不串：首字、时长、token 速度按 sessionId 独立计算。

## 15. 风险与对策

| 风险 | 对策 |
|---|---|
| Qoder 内部格式随版本变化 | 全部收敛在 `internal/qoder`，字段缺失时降级 |
| SQLite 被占用或锁 | 只读 + busy_timeout，失败跳过本轮 |
| mygo 透明 / 不抢焦点在 Windows 有差异 | M0 先验证，Win32 API 兜底 |
| 无 thinking 时首字不精确 | 使用降级值并标注 `≈` |
| token 速度含首字延迟 | 文档与 UI 中注明口径 |
| 无法定位到具体会话 | 前置窗口 + 显示项目/标题，预留深链接口 |
| 日志目录轮转 | 以最新启动目录为准，目录变化时重置 offset |
| 通知风暴 | 边沿检测、去重、上限 10、出队前重新校验 |

## 16. 后续扩展

- 接入第二个 agent：新增 Adapter，会话进入同一列表，logo 与主题色由 AgentID 决定。
- 跨 agent 切换：左右切换天然支持；通知队列同样适用。
- 可选能力：托盘常驻、开机自启、通知声音、会话置顶、点击深链。

## 17. M0 实测结论（2026-11-05）

已完成：

- mygo v0.7.4 + WebView2 透明窗口，顶部居中，展开 / 收起正常。
- 点击不抢 Qoder 焦点，验证通过。
- 左右热区点击能进入 Go 端 `Cycle`。
- 展开 560 × 224、收起 320 × 39，窗口位置随尺寸保持顶部居中。

踩到的坑：

- `DisableMove: true` 会让 mygo 在 `WM_WINDOWPOSCHANGING` 中强制 `SWP_NOMOVE`，连程序调用 `SetBounds` 也移动不了。最终不设置该选项，靠页面没有拖拽区域来防止用户移动。
- `Panel: true` 会触发 Windows 的 DWM 四角圆角，顶部无法贴边，因此不使用。改用 `AlwaysOnTop + SkipTaskbar + WS_EX_TOOLWINDOW + WS_EX_NOACTIVATE`。
- 收起态请求 34px 高，Windows 实际给 39px（窗口样式最小高度），常量已改为 39。
- 不使用 CSS `box-shadow`：阴影会渗到圆角外的透明区域，导致左右下角仍有一层颜色。只用边框和背景色。
- v0.7.4 的公开 `WindowOptions` 没有 `Focusable` 字段，不可聚焦需用 Win32 设置 `WS_EX_NOACTIVATE`。

## 18. M1 实测结论（2026-11-05）

已完成：

- `nono probe` 输出一次完整快照；`nono probe --watch` 在状态或指标变化时输出 JSON 行。
- Qoder 进程检测（`Qoder CN.exe` / `Qoder.exe`）。
- SQLite 只读查询：会话列表、最近 5 轮、parts 时间戳。
- `main.log` / `renderer.log` 的 ControlState 增量读取与基线建立。
- Segment 日志解析：请求开始/完成、token 用量、工具事件、权限事件。
- 通知队列：状态跳变入队、同会话去重更新、状态解除自动出队、上限 10。
- 单元测试覆盖：通知队列、队列上限、状态映射、token 时间窗口、项目名/模型名。

实测当前对话：

- 项目 `Nono`，状态 `执行工具 · Bash`。
- 最近 5 轮首字均值约 7.4s，平均 token 速度约 100 t/s（随对话变化）。
- live 轮次可输出当前轮时长和已完成请求的 token 速度。

关键发现：

- 数据库的 `turn_id` 与 segment 日志的 `turn_id` 不是同一套 ID，token 速度按**时间窗口**匹配，不能按 ID join。
- 首字来自 `chat_session_messages.payload_json` 中 parts 的 `startedAt`；当前轮 assistant 消息未落盘时，live TTFT 标记为 `approxTtft`。
- 通知只在首次采样建立基线之后的状态跳变产生，启动时不回放历史。
- 通知的事件时间使用采样时间；同一轮询内的多个事件按会话优先级排序。

## 19. M2 接入结论（2026-11-05）

已完成：

- 刘海页面每 700ms 调用 `Notch.Snapshot`，使用真实 Qoder 状态和指标，不再有 mock 数据。
- 收起态显示项目、状态、首字、token 速度；展开态显示标题、模型、状态详情、首字均值、首字后时长、token 速度和最近 5 轮柱状图。
- 新通知到达时自动切换到该会话，并显示 `待处理 N`；左右切换进入浏览模式，不消耗队列。
- 「去处理」调用 `FocusMainWindow` 将 Qoder 置前并出队；「跳过」只出队。
- 新增 `nono focus` 命令，便于单独验证唤起。
- 实测：Notepad 在前台时执行 `nono focus`，前台切换为 `Qoder CN`。
- 实测：当前对话显示 `Nono / 执行工具 / 首字 7.9s / 122 t/s`，与 probe 数据一致。
- 单次快照约 84ms。

要点：

- 唤起使用 `EnumWindows`：按“标题含 Qoder、是否可见、是否最小化、恢复尺寸”评分选主窗口；最小化或隐藏时先 `ShowWindow` 恢复。随后 `AttachThreadInput` 绕过前台锁，仍失败时用一次合成 Alt 键兜底。
- 最小化 / 隐藏 Qoder 均已实测可恢复并置前。
- 点击“打开 Qoder”或“去处理”成功后，刘海自动收起为 320×39。
- `nono windows` 可打印 Qoder 的顶层窗口诊断信息（PID、可见、最小化、矩形、标题）。
- UI 只有在当前会话存在通知时才显示「去处理 / 跳过」。
- 停止回合后的状态由 ControlState 的 `terminalStatus` 决定：`interrupted` → 已中断，`failed` → 失败停止，`completed` → 已完成，无终态 → 空闲。

## 20. M2 收尾与 M3 初步（2026-11-05）

已完成：

- **权限 / 提问区分**：等待审批时，如果最近工具是 `AskUserQuestion` 一类，显示“等待回复”；否则显示“权限申请 · 工具名”。这是启发式判断，未知交互仍默认权限。
- **重连状态**：解析 `QCS SSE reconnect scheduled/failed` 与 `QCS SSE connected`；空闲会话在重连期间显示“重连中”，恢复后清除；`runtimeState=error` 显示“重连失败停止”。
- **通知 FIFO**：通知时间取 ControlState / segment 的真实事件时间，队列按时间插入；队列满时丢弃最旧项。
- **切换动画**：左右切换和通知自动切换带 150ms 横向滑入。
- **托盘菜单**：显示 Nono、退出。退出入口已补齐。
- **展开态“打开 Qoder”按钮**：常驻在操作区；有通知时替换为“去处理 / 跳过”。点击后把 Qoder 置前。
- **单实例**：命名互斥量 `Local\NonoNotch`；重复启动会提示“Nono 已在运行”并退出。
- 托盘图标：`web/assets/nono-32.png`（32×32）。

验证：

- 单实例互斥量已确认存在。
- 展开态“打开 Qoder”按钮实测：Notepad 在前台时点击，前台切换为 `Qoder CN`。
- 单元测试新增通知 FIFO、提问/权限区分，全部通过。
- Windows 11 默认可能把新托盘图标收进溢出区，需要用户手动固定到任务栏。

仍未完成：

- 多会话通知队列的真实端到端测试（需要两个真实 Qoder 会话配合）。
- 配置、开机自启、打包安装包。
- 精确流式首字（当前 live TTFT 仍可能显示近似值）。
- 多 agent Adapter 抽象。

## 21. 设置页与配置（2026-11-05）

已完成：

- **右键菜单**：刘海右键弹出原生菜单（设置… / 退出）；托盘菜单同样有“设置…”。
- **设置窗口**：580×450，屏幕居中，无边框，无最小化 / 最大化 / 关闭按钮；打开设置时刘海自动收起。
- **Agent 列表**：图标、名称、探测路径、版本、是否加入刘海的开关；未安装时显示“未安装”并禁用开关。
- **保存 / 取消**：都关闭设置窗口；保存写入配置并热重载，取消不改配置。
- **配置路径**：`%APPDATA%\Nono\config.json`。
- **热重载**：关闭 Qoder 开关后刘海显示“Qoder 未启用”；重新打开后恢复真实状态，无需重启。
- **安装探测**：优先取运行中 Qoder 进程的 exe 路径，未运行时扫描 `%LOCALAPPDATA%\Programs\Qoder CN\.qoder-versions\*`；版本优先取路径中的版本号，回退到 `.qoder-app-status.json`。
- **白角**：移除了刘海 1px 浅色边框。圆角外侧现在完全透明，显示的是其后的桌面背景；如果希望圆角外不出现桌面颜色，需要改成方角或加深色衬底。

### 设置页补充（2026-11-05）

- 设置窗口改为 580×560 实心深色窗口：去掉页面外边距和阴影，窗口不透明、周围没有透明圈；使用 Windows 11 原生窗口圆角（`CornerRadius: 12`）恢复圆角。
- Agent 列表扩展为 6 个：Qoder、Codex、WorkBuddy、DeepSeek Harness、OpenCode、Claude Code。
- 探测结果：
  - Qoder `0.5.3`（已接入）
  - Codex `0.146.0-alpha.9.2`（已安装，暂未接入）
  - WorkBuddy `5.9.3`（已安装，暂未接入）
  - DeepSeek Harness `0.2.1`（已安装，暂未接入）
  - OpenCode `1.19.5`（已安装，暂未接入）
  - Claude Code 未安装（开关禁用）
- 暂未接入的 agent 会在名称旁标注“暂未接入”，开关仍然可以保存配置，但目前不会进入刘海屏；等对应 Adapter 接好后直接生效。
- 图标全部使用官方来源：Qoder 官方图标；Codex 使用 OpenAI 官方标志；OpenCode 使用 opencode.ai 官方 favicon；Claude Code 使用 claude.com 官方应用图标；WorkBuddy、DeepSeek Harness 从安装 exe 提取。
- 配置文件现在包含全部 6 个 agent，默认只有 Qoder 开启。
