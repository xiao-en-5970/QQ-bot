# Skill 总索引

> 这是 bot 全部 skill 文档的最顶层入口。`read_skill(path="")` 默认就读这一份。

读这份后，**根据用户问题决定下一步要读哪个**（不要一次性全读，progressive disclosure）：

## 可用 skill

| 入口路径 | 这是什么 | 什么场景下读 |
|---|---|---|
| `bot/SKILL.md` | **bot 自身的能力 + 行为约束**（精简索引） | 群里被触发、不知道该不该回、配置相关、自我约束 |
| `hfut-union/SKILL.md` | HFUT-Union 后端 API 索引 | 用户问"怎么登录 / 怎么发帖 / 怎么搜商品"等 HTTP API 相关问题 |

> ⚠️ **运行时禁读 hfut-union/**：bot 跟 hfut 后端的所有交互必须经过 bot 工具集封装（publish_good / off_shelf 等）；read_skill 工具会强制路径白名单。

`bot/` 子树是按主题拆分的——只读 `bot/SKILL.md` 拿索引，**真有需要再展开**子文件：

| `bot/` 子文件 | 内容 | 何时展开 |
|---|---|---|
| `bot/SKILL.md` | 主入口：5 类业务动作 + 工具速查 + Kimi 自我约束 | 几乎每次群对话开始 |
| `bot/recognition.md` | 窗口聚合 / 消歧 / 提问 vs 商品判别 / 商品去重 / 图片转存 / dispatch 限流 / Kimi prompt hard rules | 识别细节存疑时 |
| `bot/account-model.md` | 主账号 ↔ QQ 旗下号 / 接收人重定向 / 账号集 / 学校归属 / 自交易防护 | 用户问账号关系 / 通知归属时 |
| `bot/orphan.md` | 孤儿旗下号特殊行为：inbound 转发回群 / 商品请求下架 | 处理孤儿账号的商品 / 问答回复时 |
| `bot/qq-bind.md` | QQ 绑定 / 解绑 / 加急通路 / 错码锁 / 错误回执 / 服务调用审计 | 用户问绑定流程 / 加急原理 |
| `bot/verbosity.md` | 自动回复 verbose / normal 模式 | 运维 / 调试 |
| `bot/phases.md` | P0 ~ P3 实施分期归档 | 开发者回顾 |

## 推荐调用顺序

1. **任何会话开始前不熟悉 bot**：先 `read_skill(path="")` 拿到本文件 → 知道有哪些 skill 可用
2. **群里被触发要回应**：读 `bot/SKILL.md` 看个性、自动回复策略、应当遵守的原则
3. **遇到具体子主题**：再 read 对应 `bot/<topic>.md` —— **不要一次性把 bot/ 全读完**
4. 拿到答案后用 QQ 群对话风格回复，不要原样贴 markdown 表格

## Progressive Disclosure 原则

> 每次 `read_skill` 调用都会消耗模型上下文 token。**只读你这一轮真正需要的**：
>
> - 普通"出鞋架 6元"上架 → `bot/SKILL.md` + `bot/recognition.md` 已经够
> - 用户问"为啥我点赞了对方但他没收到通知" → 读 `bot/account-model.md`
> - 用户问"我绑定 QQ 显示已锁定" → 读 `bot/qq-bind.md`
> - 不要为了"以防万一"把 6 个子文件全读 —— 真不知道该读哪个时再回头读 `bot/SKILL.md` 的索引表

## skill 文件格式约定

- 顶层 SKILL.md：精简索引 + 全局约定
- 子 SKILL.md：模块详细信息（API 列表 / 行为细节 / 边界 / source 引用）
- 文件之间用相对路径互相引用（`./xxx/SKILL.md`、`../SKILL.md`）
- 文件大小尽量控制在 32KB 以内（read_skill 工具的硬上限）
