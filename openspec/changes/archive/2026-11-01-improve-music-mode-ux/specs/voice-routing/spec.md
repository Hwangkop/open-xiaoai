# Spec Delta

## MODIFIED Requirements

### Requirement: 语义路由的契约

语义路由 SHALL 依次尝试三条路径：**进程内工具路由**（一次 function calling，可直接调用全部 MCP 工具）→ **轻量分类器**（固定 intent，仅本地动作）→ **codex 路由线程**（完整工具集，兜底）。任一路径输出"已执行 / 转对话 / 忽略"三种结果之一；判为"转对话"时 SHALL 把原文交给主助手且不重复进行语音识别；全部路径都失败或返回空内容时 MUST NOT 当作"忽略"静默丢弃。

#### Scenario: 路由执行动作

- **WHEN** 用户说"豆包豆包我想听周杰伦的歌"
- **THEN** 路由调用播放工具并返回"已执行"
- **AND** 音乐立即开始播放（不等主助手回复）

#### Scenario: 路由转对话

- **WHEN** 用户说"豆包豆包现在几点了"
- **THEN** 路由返回"转对话"
- **AND** 主助手用自己的上下文回答该句

#### Scenario: 路由失败不静默

- **WHEN** 路由调用超时或返回空内容
- **THEN** 系统记录失败并回退到下一层路径
- **AND** 用户不会看到"已忽略"这种静默结果

#### Scenario: 常用指令走工具路由

- **WHEN** 用户说"豆包豆包换点安静的歌"
- **THEN** 进程内工具路由直接调用 play_music 并返回"已执行"
- **AND** 整轮耗时约 1~2 秒（不启动 codex 线程）

#### Scenario: 分类器兜底

- **WHEN** 工具路由超时或报错
- **THEN** 回退到轻量分类器，按固定 intent 本地执行

## ADDED Requirements

### Requirement: 点歌后的口头确认

路由执行点歌（play_music）后 SHALL 用一句不超过 40 字的语音确认放了哪首歌；其它动作（切歌、调音量、开关设备）MUST NOT 播报确认（本身有即时反馈）。

#### Scenario: 点歌确认

- **WHEN** 路由成功播放《慢歌》
- **THEN** 播报"正在播放《慢歌》- …"

#### Scenario: 调音量不确认

- **WHEN** 路由执行 set_volume
- **THEN** 不播报任何确认
