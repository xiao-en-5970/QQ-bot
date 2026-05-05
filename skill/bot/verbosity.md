# bot/verbosity — 自动回复模式（verbose / normal）

> 父：`bot/SKILL.md`。本文件给运维 / 调试用，Kimi 运行时不需要看。

bot 在自动监听路径下会按"识别动作"输出回执，回执有 5 类等级（`logic.ackKind`）：

| 等级 | 例子 | verbose 模式发？ | normal 模式发？ |
|---|---|---|---|
| `success` | "已为你上架二手「鞋架」：6 元（goods_id=42）" | ✅ | ✅ |
| `dup` | "你最近已经发过类似的「鞋架」（goods_id=42），没有重复上架..." | ✅ | ✅ |
| `ask_user` | "你最近在挂这几件：「A」「B」，请明确说要下架哪一个" | ✅ | ✅ |
| `fail` | "已识别到「X」，但同步到 app 失败了，稍后再试" | ✅ | ❌ 静默 |
| `ignore` | 群没配学校 / 未识别动作（none） | ❌ 静默 | ❌ 静默 |

设计动机：

- **生产环境**用户没主动喊 bot，bot 只在"产生了实际后果"才出声——`success` / `dup` / `ask_user` 都是用户**需要**知道的
- 反过来 `fail`（如 hfut 网络抖一下）在群里抛错没意义，反而让群友迷惑——`normal` 模式悄悄重试 / 完全静默更友好
- **开发调试**开 `verbose` 把所有路径都暴露，方便看 bot 在干啥
- 默认 `verbose`——忘配 / 配错时倾向"开发友好"（看得到日志）而不是"悄无声息"

切换：

```yaml
group:
  auto_reply_verbosity: normal   # 生产推荐
```

或 env：`GROUP_AUTO_REPLY_VERBOSITY=normal`，重启 bot 即生效。

注意：抑制掉的回执仍然在 zap log 里以 `INFO autoReply ack 抑制 ...` 记录，便于审计——不是真的丢了。
