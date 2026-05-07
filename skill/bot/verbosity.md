# bot/verbosity — 自动回复模式（verbose / normal）

> 父：`bot/SKILL.md`。本文件给运维 / 调试用，Kimi 运行时不需要看。

bot 在自动监听路径下会按"识别动作"输出回执，回执有 5 类等级（`logic.ackKind`）：

| 等级 | 例子 | verbose 模式发？ | normal 模式发？ |
|---|---|---|---|
| `success` | "上架成功 二手「鞋架」 6 元，食堂，配图 2 张" | ✅ | ✅ |
| `dup` | "「鞋架」已在售。要重发先回：下架旧的" | ✅ | ✅ |
| `ask_user` | "下架哪件？回数字：\n1. 鞋架\n2. 台灯" | ✅ | ✅ |
| `fail` | "「鞋架」未发出，稍后再试" | ✅ | ❌ 静默 |
| `ignore` | 群没配学校 / 未识别动作（none） | ❌ 静默 | ❌ 静默 |

**文案原则**（2026-05 P3.5 起）：

- 站在不懂代码的用户视角：不暴露 `goods_id` / `article_id` / `app` / "同步" 这类技术字眼
- 用户上架后想找回这条，自己点开 app "我的发布" 列表就行——回执只确认"做了什么 + 关键属性"
- 失败简短：「XX 未发出，稍后再试」/「下架失败，稍后再试」，不解释 hfut / 网络

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
