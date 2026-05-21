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

---

## SilentMode（灰度静默模式）

`GROUP_AUTO_REPLY_VERBOSITY` 控制的是「回执文案要不要发」；`BOT_SILENT_MODE` 更狠
——开了之后 bot **不在非运维群讲任何一句话**，也不发任何私聊。用于"上线前真实校园
群灰度演练"：观察识别准确率，但群友完全感受不到 bot 存在。

| 出口 | SilentMode=false | SilentMode=true |
|---|---|---|
| 群消息 → 运维群（`OpsGroupIDs`） | 发 | **照常发**（运维提醒 / `NotifyOpsPublish` / 运维 @bot 查询回复） |
| 群消息 → 其它群（识别 ack / @bot 命令 / dup ack / hfut 反向 send-group） | 发 | **静默** |
| 私聊（QQ 绑定验证码 / 群接入回执 / 订单加急私聊） | 发 | **静默** |
| 群文件上传（jm pdf / pix） | 发 | **静默** |
| hfut 数据库写入（识别成功后落 goods/articles 等） | 写 | **照常写** |
| Kimi 识别调用 / Redis 防抖 / metric_minute 计数 | 跑 | **照常跑** |

开法：

```bash
export BOT_SILENT_MODE=true
```

或 `conf` yaml：

```yaml
bot:
  silent_mode: true
```

被静默掉的每次发送都会 `IncSilentSuppressed` 计数器 +1，metric_minute 表里
`metric='silent_suppressed'` 有按分钟聚合的数据，运维面板上能看到"今天屏蔽了
多少条"。zap log 也有 `[silent] 屏蔽群消息 group=... text=...` 的 debug 记录。

切回正式上线时把 env 改回 `false`（或删掉），重启 bot 即生效。
