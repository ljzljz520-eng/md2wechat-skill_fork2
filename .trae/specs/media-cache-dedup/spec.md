# 微信媒体资产缓存与去重（Media Cache & Dedup）- Product Requirements Document

## Overview
- **Summary**: 在发布链路中新增本地媒体缓存层：基于「原始 blob digest + 标准化 pHash + 规范化处理参数」派生 key，按微信账号维护素材记录（media_id / URL / 创建时间 / 有效性 / 引用追踪 / 缩略参考图）；发布前命中验证、失效自动重传；提供 manifest pin、GC、账号隔离与近重复提示。
- **Purpose**: 当前所有上传路径（convert --upload/--draft、image-post、封面、独立 upload 命令）每次都无条件调用微信 `add_material`，重复上传相同/近似图片浪费永久素材额度、拖慢发布，且 media_id 被删除后只能人工发现。缓存层让「字节相同 + 参数相同」的资产在同一账号下只上传一次，并保证跨参数、跨账号不串用。
- **Target Users**: 使用 md2wechat 向微信公众号发布图文的终端用户，以及编排发布流程的 Agent。

## Goals
- 为每个原始资产计算 SHA-256 blob digest 与 64-bit 标准化 pHash。
- 派生 key 纳入全部处理参数（压缩开关、最大宽、最大字节、JPEG 质量、目标格式、裁剪、pipeline 类型）。
- 按微信账号隔离维护完整素材记录与引用关系、缩略参考图。
- 发布前命中验证：TTL 内信任、过期经 `get_material` 探测、失效（含被动发现）自动重传。
- 发布成功后写 manifest pin；提供显式 GC；被发布 manifest 引用的 blob 不被回收。
- 同字节资产并发上传 single-flight，恰好上传一次。
- 基于 pHash Hamming 距离提供**建议性**近重复提示。
- 提供 `md2wechat media` 命令组进行缓存管理。

## Non-Goals
- 不做微信端素材的删除、批量列表同步或素材库远程管理。
- 不自动 GC；GC 仅通过显式命令执行。
- 近重复判断不阻断发布、不驱动确定性 CLI 路由（遵守 AGENTS.md Anti-Noise 规则）。
- 不覆盖视频、语音、缩略 thumb 等非图片素材类型。
- 不改变转换、主题、layout 的既有行为与输出。
- 不新增第三方 Go 依赖（DCT/pHash 用标准库自实现，图像解码复用已有的 disintegration/imaging）。

## Background & Context
- 现有上传链路：
  - 内容图统一经 `internal/image.Processor`（`UploadLocalImage` / `DownloadAndUpload` / `GenerateAndUpload`），最终调用注入的 `UploadFunc`；压缩由 `internal/image/compress.go` 完成（质量固定 85，png/jpg 保格式，其余转 jpeg，压缩后变大则回退原图）。
  - 封面上传 `uploadCoverImage`（cmd/md2wechat/convert.go）直接调用 `wechat.Service.UploadMaterial`，绕开 Processor。
  - 运行时装配点为 `cmd/md2wechat/image_runtime.go`。
- 微信永久图片素材 media_id 在被手动删除前长期有效，微信端不做内容去重（同字节重复上传会得到不同 media_id）。
- 微信 SDK v2.1.9 未封装图片的 get_material 探测（仅有 `GetNews`），需按 `CreateNewspicDraft` 的既有模式直接 HTTP 调用 `cgi-bin/material/get_material`：图片返回二进制流；失效时返回含 errcode（如 40007）的 JSON。
- 仓库已确认无既存 media cache / manifest / digest 机制。
- 用户已确认：缓存位于全局 `~/.config/md2wechat/media-cache/`；混合命中验证（TTL 内信任，过期探测）；新增 `media` 命令组；「引用图」同时包含引用追踪清单与缩略参考图。

## Functional Requirements

- **FR-1 原始 blob digest**：对原始输入字节（本地文件、远程下载落盘字节、AI 图下载落盘字节）计算 SHA-256；相同字节必然得到相同 digest。
- **FR-2 标准化 pHash**：解码原图 → 灰度 → 标准化缩放 → DCT → 取左上 8×8（排除 DC 分量）→ 中位数阈值 → 64 bit；GIF 取首帧；记录 `phash_version`；不可解码时 phash 置空且不阻断缓存与上传。
- **FR-3 派生 key**：定义规范化 ProcessSpec（`pipeline_kind`、`compress_enabled`、`max_width`、`max_size_bytes`、`jpeg_quality`、`output_format`、`crop`、`processor_version`），以 canonical JSON 与 source digest 一起做 SHA-256；任一字段变化即产生不同 key。
- **FR-4 管道确定性**：给定原始字节与 ProcessSpec，「是否实际压缩 / 是否回退原图」是固定函数（阈值确定），因此 key 与实际上传字节一一对应；记录中保存**实际上传字节**的 blob digest。
- **FR-5 账号隔离存储**：缓存根目录 `~/.config/md2wechat/media-cache/`（0700），含 content-addressed `blobs/`、`thumbs/`、索引、`manifests/`、锁；账号键：命名账号用账号名，否则用 AppID；不同账号记录互不命中。
- **FR-6 MediaRecord**：字段含 derived_key、account_key、source_digest、source_phash、phash_version、process_spec、uploaded_blob_digest、media_id、wechat_url、created_at、last_validated_at、status（valid/expired/invalid）、invalid_reason、引用方清单、manifest_pins；索引原子写并加文件锁。
- **FR-7 缩略参考图**：为每个 source digest 生成一张固定宽度（默认 256px）JPEG（默认 q70）缩略图，存于 `thumbs/`，供人工/Agent 比对。
- **FR-8 命中验证与复用**：上传前计算身份与 key 并查记录——valid 且在 TTL（默认 7 天）内直接复用、零上传；超过 TTL 调用 get_material 探测，成功则刷新 last_validated_at 复用，探测失效则标记 invalid 并重传。
- **FR-9 失效重传与被动失效**：未命中或 invalid 时执行处理并上传、写 valid 记录；草稿/发布返回失效类错误码（如 40007）时把对应记录标记 invalid；内容图场景允许一次有界重试（重新处理资产并重建草稿）。
- **FR-10 single-flight**：同进程内同 derived key 的并发请求合并为一次实际上传；跨进程通过文件锁与原子索引写保证一致。
- **FR-11 manifest pin**：草稿创建成功后生成 manifest（manifest_id、account_key、created_at、来源标识、items[]：derived_key/source_digest/uploaded_blob_digest/media_id/wechat_url），写入 `manifests/` 并把 manifest_id 回填记录；相同内容重复发布幂等。
- **FR-12 GC**：`media gc` 默认 dry-run，需 `--yes` 才实际删除；保留集 = 所有 valid 记录引用的 blob ∪ 所有 manifest 引用的 blob（即使其记录已 invalid/expired）；清理无引用 blob 与超宽限期的无 pin invalid 记录；输出统计，支持 --json。
- **FR-13 近重复提示**：上传时以 pHash 比对同账号记录（可用 flag 扩到跨账号），Hamming 距离 ≤ 阈值（默认 5/64）输出建议性提示；仅出现在日志、`media near-dupes` 命令与 JSON advisory 字段，不阻断、不改路由。
- **FR-14 media 命令组**：`list` / `show` / `validate` / `gc` / `near-dupes`，支持 `--account`、`--json` 及各自参数；遵循现有 JSON envelope 与退出码约定；只读命令无网络/写入副作用（validate 探测除外）。
- **FR-15 全路径集成**：Processor 三条路径与封面上传全部经缓存层；身份在处理前计算，上传经 Resolve 完成。
- **FR-16 降级策略**：缓存目录不可用/初始化失败时发出警告并 fail-open（跳过缓存直传），不阻断发布；phash 计算失败不阻断。

## Non-Functional Requirements
- **NFR-1 可确定性测试**：全部判定离线可测；微信上传与 get_material 探测经接口注入，默认回归套件无网络依赖。
- **NFR-2 并发安全**：N 个相同字节并发请求 → 底层恰好 1 次上传，全部得到同一 media_id。
- **NFR-3 性能**：≤10MB 图像的 digest+phash 计算应在百毫秒量级；缓存命中零网络请求。
- **NFR-4 安全**：缓存目录 0700；日志沿用既有 media_id mask；manifest 与缓存不含 Secret；探测/上传仅发往微信域名。
- **NFR-5 兼容**：无图片、纯本地转换、discovery 类命令行为完全不变；缓存默认启用但不引入新的凭证要求。
- **NFR-6 可观测**：命中、探测、重传、失效、GC 均有结构化日志（derived_key 前缀、account、reason）。

## Constraints
- **Technical**：Go 1.26；仅使用现有依赖；DCT 与 pHash 自实现；原子写复用 internal/atomicfile。
- **Business**：遵守 AGENTS.md——确定性规则只基于可观测结构与显式状态；近重复仅建议；GC 显式；只读命令无副作用。
- **Dependencies**：微信 `add_material` 与 `get_material` HTTP 接口；disintegration/imaging 解码能力。

## Assumptions
- AddMaterial 上传的是永久图片素材；同账号重复上传相同字节会产生不同 media_id。
- 远程/AI 图在处理前已可被本地读取原始字节。
- 默认参数：TTL 7 天；缩略图 256px / JPEG q70；近重复阈值 Hamming ≤ 5；GC invalid 记录宽限期 7 天。
- get_material 对图片返回二进制流，对失效 media_id 返回非零 errcode JSON。

## Acceptance Criteria

### AC-1: 原始 blob digest 正确且确定
- **Type**: `rule`
- **Given**: 任意源图像的原始字节
- **When**: 计算 blob digest
- **Then**: 输出为该字节 SHA-256 十六进制；相同字节（不同文件名/路径/修改时间）结果相同；单字节变化结果不同
- **Pass Condition**: 表驱动用例（含重复字节不同文件名）全部通过
- **Evidence**: `internal/mediacache` 单测断言

### AC-2: 标准化 pHash 确定且可降级
- **Type**: `rule`
- **Given**: 可解码图像、GIF、不可解码但扩展名合法的文件
- **When**: 计算 pHash
- **Then**: 同字节 pHash 恒定；记录携带 phash_version；GIF 取首帧得到稳定值；不可解码时 pHash 为空且返回无错误
- **Pass Condition**: 三类样本断言全部成立
- **Evidence**: pHash 单测（含 GIF fixture）

### AC-3: 派生 key 对处理参数敏感
- **Type**: `rule`
- **Given**: 同一原始字节与多组 ProcessSpec
- **When**: 计算派生 key
- **Then**: 同 spec → 同 key；改变 pipeline_kind / compress_enabled / max_width / max_size_bytes / jpeg_quality / output_format / crop 任一项 → key 不同
- **Pass Condition**: 参数矩阵每一格断言成立
- **Evidence**: 矩阵单测

### AC-4: 账号隔离
- **Type**: `rule`
- **Given**: 同字节同参数资产分别在账号 A、B 发布
- **When**: 账号 B 发起解析
- **Then**: 不命中账号 A 的记录；两个账号各自存在独立记录；list --account 只返回对应账号
- **Pass Condition**: 跨账号零命中，记录数为 2
- **Evidence**: 缓存服务单测

### AC-5: MediaRecord 字段完整持久化（含引用追踪与缩略图）
- **Type**: `rule`
- **Given**: 一次成功上传
- **When**: 写入后重新加载索引并 show
- **Then**: media_id、wechat_url、created_at、last_validated_at、status、uploaded_blob_digest、引用方清单、phash、process_spec 均可读回；blobs 与 thumbs 下对应文件存在
- **Pass Condition**: 字段逐一断言且两个文件存在
- **Evidence**: 存储层单测

### AC-6: TTL 内命中零上传
- **Type**: `rule`
- **Given**: 存在 valid 且 last_validated_at 在 TTL 内的记录
- **When**: 再次发布同一资产
- **Then**: 不调用上传、不调用探测；返回同一 media_id/URL
- **Pass Condition**: mock 上传调用次数 = 0
- **Evidence**: Resolve 单测

### AC-7: TTL 过期探测后复用或重传
- **Type**: `rule`
- **Given**: 记录 last_validated_at 已过 TTL
- **When**: 再次发布
- **Then**: get_material 探测成功 → 刷新 last_validated_at 且上传次数 0；探测返回失效 errcode → 记录变 invalid、执行一次重传并得到新 media_id
- **Pass Condition**: 两个分支分别断言 probe=1/upload=0 与 probe=1/upload=1
- **Evidence**: 探测分支单测

### AC-8: 被动失效自动标记并重试
- **Type**: `rule`
- **Given**: 草稿/发布接口对已用 media_id 返回失效类错误码（如 40007）
- **When**: 发布流程收到该错误
- **Then**: 对应记录被标记 invalid（含 invalid_reason）；内容图场景触发一次且仅一次有界重试，重试经重传后成功
- **Pass Condition**: 记录状态为 invalid，重试成功且无第二次重试
- **Evidence**: 发布服务集成单测（错误注入）

### AC-9: 字节相同资产并发只上传一次
- **Type**: `rule`
- **Given**: N 个 goroutine 同时发布字节完全相同、参数相同的资产
- **When**: 并发解析
- **Then**: 底层上传恰好被调用 1 次；所有调用方拿到同一 media_id；仅一条记录
- **Pass Condition**: 上传计数 = 1（多轮 N=2/8/32）
- **Evidence**: single-flight 并发单测

### AC-10: 不同处理参数不被错误复用
- **Type**: `rule`
- **Given**: 同字节资产携带不同 ProcessSpec（并发或串行）
- **When**: 分别解析
- **Then**: 各 key 独立上传、独立记录，结果与其参数一一对应，不存在跨 key 复用
- **Pass Condition**: 上传次数 = 不同参数组数；每条返回 media_id 与其 key 绑定正确
- **Evidence**: 混合参数并发矩阵单测

### AC-11: manifest pin 落盘、回填且幂等
- **Type**: `rule`
- **Given**: 一次成功的草稿创建
- **When**: 生成 manifest
- **Then**: manifests/ 下文件字段完整（account、items 三个 digest/id/url 齐全）；相关记录 manifest_pins 被回填；以相同 items 再次发布不新增 manifest
- **Pass Condition**: 文件存在、字段断言通过、重复发布 manifest 数不变
- **Evidence**: manifest 单测

### AC-12: GC 不删除被发布 manifest 引用的 blob
- **Type**: `rule`
- **Given**: 若干 blob：被 valid 记录引用、被 manifest 引用但记录已 invalid、无任何引用
- **When**: 执行 media gc（先 dry-run 再 --yes）
- **Then**: dry-run 不删除任何文件；--yes 后仅无引用 blob 被删；manifest 引用（含仅 manifest pin）的 blob 全部保留
- **Pass Condition**: 删除/保留集合与预期完全一致
- **Evidence**: GC 单测

### AC-13: GC 按宽限期清理失效记录
- **Type**: `rule`
- **Given**: invalid 记录分别为「超宽限期且无 manifest pin」「未超宽限期」「有 pin」
- **When**: 执行 media gc --yes
- **Then**: 仅第一类记录被清理；valid 记录与有 pin 记录不动
- **Pass Condition**: 记录存留集合与预期一致
- **Evidence**: GC 单测

### AC-14: media 命令组契约稳定
- **Type**: `rule`
- **Given**: 含若干记录的缓存
- **When**: 运行 list/show/validate/gc/near-dupes（含 --json 与非法参数）
- **Then**: JSON envelope 与现有命令一致；退出码合法（非法输入非零）；list/show/gc(dry-run)/near-dupes 无写入与上传副作用
- **Pass Condition**: 契约断言与副作用断言通过
- **Evidence**: CLI 契约测试

### AC-15: 近重复提示建议性与准确度
- **Type**: `rubric`
- **Dimension**: 近重复提示在「该出现时出现、不该出现时安静、且永不阻断发布」上的表现
- **Scale**: 1-5
- **Anchors**: 1 = 阻断发布或大量误报噪音；3 = 能提示但有漏报/误报或提示位置干扰主流程；5 = 阈值内（如缩放、轻微压缩、小裁剪）稳定提示，阈值外（不同图）安静，纯 advisory 不影响退出码与发布结果
- **Pass Threshold**: >= 4
- **Evidence**: 近重复样本集（同图变换 vs 不同图）的命令/日志输出评估

### AC-16: 全部上传路径经缓存层
- **Type**: `rule`
- **Given**: local、remote、AI 内容图与 cover 四种来源
- **When**: 分别走发布流程
- **Then**: 四者均先算身份再经缓存 Resolve；相同资产二次发布均命中；cover 以 pipeline_kind=cover 独立成 key
- **Pass Condition**: 四条路径上传计数与 key 断言全部成立
- **Evidence**: Processor 与封面链路单测

### AC-17: 安全、降级与默认行为兼容
- **Type**: `rule`
- **Given**: 缓存目录不可写、无图片的转换、discovery 命令、未命名账号
- **When**: 分别执行
- **Then**: 初始化失败有警告且 fail-open 直传成功；无图/无发布意图时无缓存读写；日志中 media_id 被 mask；目录权限 0700
- **Pass Condition**: 各场景断言成立
- **Evidence**: 降级与兼容单测、文件权限检查

### AC-18: 工程质量与文档同步
- **Type**: `rubric`
- **Dimension**: 代码格式、静态检查、质量门禁与文档/技能同步的完整度
- **Scale**: 1-5
- **Anchors**: 1 = 门禁失败或文档缺失；3 = 代码与测试通过但文档/技能未同步；5 = gofmt/vet/`make quality-gates` 全绿，CONFIG/FAQ/DISCOVERY 与两处 SKILL.md 已同步且仅写必要内容
- **Pass Threshold**: >= 4
- **Evidence**: 门禁输出与文档 diff

## Open Questions
- 无（四个关键决策已由用户确认；具体字段命名与内部结构在 Plan 阶段确定）。
