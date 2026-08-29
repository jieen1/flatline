# ADR-25: 舰队汇总——父会话及其子代理树是一等展示单元

- 状态：accepted
- 日期：2026-08-29
- 决策者：用户（"找到系统的核心价值，规划有意义的功能"）+ 主 agent 提案

## 背景

本机 977 个会话里 679 个（69%）是子代理；每周 4–10 个"主会话带 ≥3 子代理"的舰队，
最大单舰队 108 个子代理、父+子 6.2B token（全机 18%）。用户的真实工作单元是
"一次舰队运行"，但系统把它呈现为一堆孤立的会话行：会话详情只有 `subagent_count`
一个数字，孩子的角色、代价、摩擦、结局要用户自己拼 `/sessions?parent=…` 聚合。

同时，token 总量作为页面头号指标失真严重：全库 98% 的 token 是缓存读取
（47.0B / 48.0B），真实工作量（input + output + cache write）只有 970M。
"378M token 的会话"实际输出 445K token。四个分量都已在库（ADR-22），只是展示选错了领衔者。

外部参照：LangSmith 2026 新增 Fleet（舰队管理 + 统一成本视图），AgentOps 做 agent 群轨迹
——均要求 SDK/网关接入。"零接入读本地转写"的位置没有竞争者。

## 决策

1. **新端点 `GET /api/v1/sessions/{id}/fleet`**（读时聚合，无新表、无迁移，ADR-20 纪律）：
   - `children[]`：每个子会话的角色、display_title、token 四分量、摩擦数、时长、in_progress；
   - `rollup`：父+子的四个 token 分量分别求和；`work_tokens = input + output + cache_write`；
     摩擦合计；编辑行/改动文件合计；
   - `outcome`：树内记录到的 `git commit / push / merge` 命令数与"未记录到失败"的条数。
     claude_code 的命令 98% 没有退出码——**只说"记录到 N 次、M 次未见失败"，不说成功**（ADR-8）。
2. **会话详情页"团队"区块**：`subagent_count > 0` 时渲染，孩子按 token 降序，
   区块头领衔"工作 token"，总量与缓存读取并列在后。
3. **display_title 剥离 harness 包装**（65 个会话，6.7%）：`title` 命中
   `canonical.InjectedMessagePrefixes` 时，依次取标签 `summary` 属性 → 包装内首行非空文本，
   `title_source` 改标 `synthesized`。库中原 title 不动。

## 后果

- 正向：用户第一次能直接回答"这次舰队运行的组成、代价与结局证据"；
  "工作 token"给了一个不失真 50 倍的代价读数；孩子行可读。
- 代价：fleet 端点对大舰队（108 孩子）是一次 O(children) 聚合查询——children 已有
  parent_session_id 索引，实测规模（≤200）远低于需要分页的量级，暂不分页。
- 不做：跨父子的"舰队列表页"（先在详情页验证价值）；美元换算（外部易变事实）；
  成败判定（证据纪律禁止，只陈述记录到的结局证据）。
