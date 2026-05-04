# Skill 总索引

> 这是 bot 全部 skill 文档的最顶层入口。`read_skill(path="")` 默认就读这一份。

读这份后，**根据用户问题决定下一步要读哪个**（不要一次性全读，progressive disclosure）：

## 可用 skill

| 入口路径 | 这是什么 | 什么场景下读 |
|---|---|---|
| `bot/SKILL.md` | **bot 自身的能力 + 行为约束** | 群里被触发、不知道该不该回、配置相关、自我约束 |
| `hfut-union/SKILL.md` | HFUT-Union 后端 API 索引 | 用户问"怎么登录 / 怎么发帖 / 怎么搜商品"等 HTTP API 相关问题 |

后续会加更多 skill（每个对应一个上游系统或一组业务能力），都在 `skill/` 下各自的目录里。

## 推荐调用顺序

1. **任何会话开始前不熟悉 bot**：先 `read_skill(path="")` 拿到本文件 → 知道有哪些 skill 可用
2. **群里被触发要回应**：读 `bot/SKILL.md` 看个性、自动回复策略、应当遵守的原则
3. **用户问 HFUT 项目相关**：读 `hfut-union/SKILL.md` 看模块清单 → 再读 1-2 个最相关的子 skill（如 `hfut-union/post/SKILL.md`）
4. 拿到答案后用 QQ 群对话风格回复，不要原样贴 markdown 表格

## skill 文件格式约定

- 顶层 SKILL.md：精简索引 + 全局约定
- 子 SKILL.md：模块详细信息（API 列表 / 行为细节 / 边界 / source 引用）
- 文件之间用相对路径互相引用（`./xxx/SKILL.md`、`../SKILL.md`）
- 文件大小尽量控制在 32KB 以内（read_skill 工具的硬上限）
