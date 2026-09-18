# 微信媒体资产缓存与去重 - Implementation Plan

说明：新增内聚包 `internal/mediacache`（digest / phash / identity / process_spec / record / store / manifest / cache / pin / gc），上传与探测经接口注入；集成点为 `internal/image/processor.go`、`internal/wechat/service.go`、cmd 层装配与新 `media` 命令。每个 Task 至少一条 TR，全部 AC 均有覆盖。

## Task 1: get_material 探测与失效错误码分类
- **Status**: `completed`
- **Priority**: high
- **Depends On**: None
- **Completion Evidence**:
  - TR-1.1（rule）：`internal/wechat/material_probe_test.go` 8 个响应场景全过——二进制/errcode=0→true；40007/40009→false,nil；网络错误/未知 errcode/非 200/空体→false,err；另有空 mediaID 零网络与注入接缝测试。命令：`GOCACHE=/tmp/md2wechat-go-build go test ./internal/wechat/ -run 'MaterialExists|IsInvalidMediaError'` → ok。
  - TR-1.2（rule）：IsInvalidMediaError 表驱动 7 例（nil/40007/40009/45002/网络错误/裸 40007/无关消息）断言全过。gofmt clean。
- **Description**:
  - 在 `internal/wechat/service.go` 新增 `MaterialExists(ctx, mediaID) (bool, error)`：按 `CreateNewspicDraft` 既有模式直接 POST `cgi-bin/material/get_material`，复用 service httpClient 与 `withWechatSDKHTTPClient`；二进制响应（图片存在）→ true；JSON 且 errcode=0 → true；errcode 为失效类（40007 invalid media_id、40009）→ false,nil；网络/HTTP 错误 → false,err（探测保守失败，不误判）。
  - 新增 `IsInvalidMediaError(err) bool` 与失效码常量，供被动失效路径复用。
  - 探测函数保留可注入接缝，供测试替身使用。
- **Acceptance Criteria Addressed**: AC-7（探测侧）、AC-8（错误码识别侧）
- **Test Requirements**:
  - `rule` TR-1.1: 用注入接缝模拟「二进制流 / errcode=0 JSON / errcode=40007 / errcode=40009 / 网络超时」五种响应，断言分别为 true,nil / true,nil / false,nil / false,nil / false,非nil；证据：`internal/wechat` 探测单测。
  - `rule` TR-1.2: 表驱动验证 IsInvalidMediaError 仅对失效类错误码为 true，对 45002、普通网络错误、nil 为 false；证据：单测断言。

## Task 2: 原始 digest、标准化 pHash 与缩略参考图
- **Status**: `completed`
- **Priority**: high
- **Depends On**: None
- **Completion Evidence**:
  - TR-2.1（rule）：digest 与 sha256("") 标准向量一致；同字节（独立副本）digest 相同；单字节变化 digest 不同；BlobDigest 与 DigestBytes 一致。`go test ./internal/mediacache/` → ok。
  - TR-2.2（rule）：pHash 确定性（同字节两次一致）、v/h 图案可区分、PHashVersion 非空、GIF 首帧与首帧 PNG 完全一致、不可解码字节返回 ErrImageDecode 哨兵且 hash=0、ComputeIdentity 对垃圾字节降级（phash 空、digest 在、无错误）。
  - TR-2.3（rule）：GenerateThumb 大图 600×300→256×128 可解码 JPEG，小图保持 120 宽，不可解码输入报错。
  - 关键调优：阈值由中位数改为经典 pHash 均值（排除 DC）后，300px 缩放副本 Hamming 距离 = 0（中位数时 10）。gofmt clean。
- **Description**:
  - `internal/mediacache/digest.go`：`BlobDigest(r io.Reader) (hex string, err error)`（SHA-256）。
  - `internal/mediacache/phash.go`：自实现 32×32 灰度标准化 + 一维/二维 DCT（标准库，无第三方依赖），取左上 8×8 排除 DC，中位数阈值得 64-bit；常量 `PHashVersion`；GIF 首帧（imaging 解码即首帧）；解码失败返回空 hash + 哨兵错误供上层降级。
  - `internal/mediacache/identity.go`：`Identity{SourceDigest, PHash, PHashVersion}`，`ComputeIdentity(originalBytes)`；`GenerateThumb(originalBytes) ([]byte, error)`（256px 宽 JPEG q70）。
- **Acceptance Criteria Addressed**: AC-1、AC-2、AC-5（thumbs 生成侧）
- **Test Requirements**:
  - `rule` TR-2.1: 相同字节（不同文件名/mtime）digest 相同且等于独立 SHA-256 向量；改动 1 字节 digest 不同；证据：digest 单测。
  - `rule` TR-2.2: 同字节 pHash 恒定、PHashVersion 非空；GIF fixture 取首帧稳定；构造不可解码字节流时 pHash 为空、错误可识别且不 panic；证据：phash 单测。
  - `rule` TR-2.3: GenerateThumb 输出可被 jpeg 解码、宽度 ≤256 且等比；不可解码输入返回错误；证据：identity 单测。

## Task 3: ProcessSpec 规范化与派生 key
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 2
- **Completion Evidence**:
  - TR-3.1（rule）：参数矩阵全过——baseline 重复 key 一致；pipeline_kind/compress/max_width/max_size/jpeg_quality/output_format/crop/processor_version 8 个变体 key 全部不同于基线；source digest 变化 key 变化。
  - TR-3.2（rule）：CanonicalJSON 多次输出字节一致；ForContent 字段映射正确，ForCover 保持无压缩直传语义，nil cfg 不 panic。gofmt clean。
- **Description**:
  - `internal/mediacache/process_spec.go`：ProcessSpec 字段（pipeline_kind、compress_enabled、max_width、max_size_bytes、jpeg_quality、output_format、crop、processor_version）；`CanonicalJSON()`（字段序固定、空值显式）；`DerivedKey(sourceDigest string) string` = SHA-256 hex(canonical + sourceDigest)。
  - `ForContent(cfg)` / `ForCover(cfg)` 从 config 与压缩器现状（质量 85、格式规则）构造；常量 `ProcessorVersion`，算法/规则变更时升版。
- **Acceptance Criteria Addressed**: AC-3、AC-4（输入侧确定性）
- **Test Requirements**:
  - `rule` TR-3.1: 参数矩阵——基线 spec 与逐字段变更（pipeline_kind/compress/max_width/max_size/quality/output_format/crop）的 derived key：基线自身相同，其余全部不同；证据：矩阵单测。
  - `rule` TR-3.2: CanonicalJSON 对同一 spec 多次输出字节一致；ForContent/ForCover 映射 cfg 各字段正确且 pipeline_kind 不同；证据：单测断言。

## Task 4: 缓存配置项与存储层（blobs/thumbs/index/manifests/lock）
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 2、Task 3
- **Completion Evidence**:
  - TR-4.1（rule）：Open 创建 root/blobs/thumbs/manifests/locks 全部 0700；PutBlob 幂等（同 digest 等长不重写；只读文件二次写不报错；长度不同会重写）；aa 分片正确；blob 文件 0600；ReadBlob 缺失→ErrNotFound；thumb 存取、manifest CRUD/幂等删除全过。
  - TR-4.2（rule）：跨进程锁双实现（unix.Flock + windows LockFileEx，`GOOS=windows go build` 通过）；持锁时第二句柄超时→ErrLockBusy，释放后立即可取；8 协程持锁读改写 index 无丢失更新。
  - TR-4.3（rule）：index 原子写+roundtrip；写入 `{not json` 后 ReadIndex 返回损坏错误而非静默重置；version 校验。
  - TR-4.4（rule）：cache YAML 段（enabled/dir/ttl_days/grace_days/near_duplicate_threshold）映射、env（MD2WECHAT_CACHE_*）覆盖文件、默认值 7/7/5、非法值（负 TTL/grace、阈值>64、空 dir）拒绝、SaveConfig→Load roundtrip 全过。gofmt clean。
- **Description**:
  - 在 `internal/config` 增加缓存配置（建议命名空间 `cache`：dir、enabled、ttl_days、grace_days、near_duplicate_threshold；env 如 `MD2WECHAT_CACHE_DIR` 等），同步 configFile 映射、ToMap、默认值（dir=~/.config/md2wechat/media-cache，ttl 7，grace 7，threshold 5）。
  - `internal/mediacache/record.go`：MediaRecord 模型（spec FR-6 全字段，引用方清单为 []struct{source...}）。
  - `internal/mediacache/store.go`：根目录初始化（0700）、`blobs/aa/<sha>` 分片路径、PutBlob（幂等写）、Thumb 写入、index.json 的加锁读/原子写（复用 atomicfile）、跨进程文件锁。
  - `internal/mediacache/manifest.go`：Manifest/ManifestItem 模型与 manifests 读写（仅模型与存取，pin 编排归 Task 6）。
  - 账号键解析：命名账号 → 账号名；否则 → AppID。
- **Acceptance Criteria Addressed**: AC-4、AC-5、AC-17（权限/配置侧）
- **Test Requirements**:
  - `rule` TR-4.1: Open 后根目录权限为 0700 且子目录齐全；PutBlob 同字节重复写只落一份、路径与 digest 一致；证据：临时目录存储单测 + 权限位断言。
  - `rule` TR-4.2: 写→重新加载索引记录一致；人为截断索引文件后加载返回明确错误；文件锁使两个 Store 句柄的写区序列化（用 try-lock 验证）；证据：存储/锁单测。
  - `rule` TR-4.3: Manifest 模型 marshal/unmarshal 后字段无损；证据：manifest 模型单测。
  - `rule` TR-4.4: cache 配置三层（YAML key / env / ToMap key）命名清晰、默认值正确，SaveConfig→Load 往返一致；证据：config 单测。

## Task 5: 缓存 Resolve：命中/TTL/探测/重传 + single-flight + 近重复计算
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 1、Task 4
- **Completion Evidence**:
  - TR-5.1（rule）：TTL 内命中 upload=0/probe=0（首传 upload=1/probe=0，CacheHit=true）。
  - TR-5.2（rule）：8 天后 probe=true→probe=1/upload=1 总数、复用原 media_id；probe=false（服务端已删）→probe=2/upload=2、Reuploaded=true、新 id 替换记录；invalid 记录不探测直接重传；probe 网络错误 fail-closed（ErrProbeUnavailable，不重传）。
  - TR-5.3（rule）：`-race` 下 N=2/8/32 同 key 冷启动并发 upload 计数恒为 1，全部 follower 同 media_id。
  - TR-5.4（rule）：base/MaxWidth 变体/cover + 异账号共 4 次上传，无跨 spec/跨账号复用。
  - TR-5.5（rule）：300px→64px 同图 miss 时 Advisory 命中（距离 0）且上传照常；确定性噪声图不报；threshold=0 关闭；MarkInvalid 按账号隔离。gofmt clean。
- **Description**:
  - `internal/mediacache/cache.go`：`Service`（持有 Store、Uploader 接口 `Upload(processedPath)→(mediaID,url,error)`、Prober 接口 `MaterialExists`、时钟）。
  - `Resolve(req)`：计算/接收 identity 与 ProcessSpec → derived key → 查同账号记录；valid 且未过 TTL → 直接返回（零上传、零探测）；过 TTL → Prober：成功刷新 last_validated_at 复用，失效置 invalid 后走上传分支；无记录/invalid → single-flight 执行 Uploader，写入 blob/thumb/记录（status=valid、created_at/last_validated_at）。
  - in-process single-flight：map[derivedKey]*inflight（WaitGroup/channel 结果共享）；跨进程由 Store 锁兜底。
  - 近重复：插入/命中时以 pHash 对同账号记录算 Hamming 距离，≤threshold 的条目作为 `Advisory`（manifest_pins/key/距离）挂到 ResolveResult；纯数据，不影响 error。
- **Acceptance Criteria Addressed**: AC-6、AC-7、AC-9、AC-10、AC-15（建议生成侧）
- **Test Requirements**:
  - `rule` TR-5.1: 未过期命中：Uploader 与 Prober 调用次数均为 0，返回 media_id 与记录一致；证据：Resolve 单测。
  - `rule` TR-5.2: 过期两分支：Prober=true→probe=1/upload=0 且 last_validated_at 被刷新；Prober=false→probe=1/upload=1、旧记录 invalid、新 media_id 返回；证据：分支单测。
  - `rule` TR-5.3: N=2/8/32 goroutine 同 key 并发冷启动，Uploader 计数恰好 1、全部得到同一 media_id、仅一条记录；证据：并发单测（-race 下运行）。
  - `rule` TR-5.4: 同字节不同 ProcessSpec（2-4 组）并发：上传次数 = 参数组数，每组 media_id 与其 key 绑定，无跨组复用；证据：混合参数并发矩阵。
  - `rule` TR-5.5: 同图变换样本（缩放/轻压）Advisory 非空且距离 ≤threshold，不同图样本 Advisory 为空；无论是否有近重复，Resolve 都不返回错误、不改变上传结果；证据：近重复规则单测。

## Task 6: manifest pin 编排（落盘 + 回填 + 幂等）
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 5
- **Completion Evidence**:
  - TR-6.1（rule）：ManifestID 由 account+排序 derived keys 确定性生成（顺序无关、账号敏感、空项/空 key 拒绝）；首 Pin manifest 落盘（items 排序、source/account 齐全）、两条记录 manifest_pins 回填；打乱顺序重复 Pin 返回同 id，manifests 文件数仍为 1；缺 digest/media_id 的 item 被拒绝。gofmt clean。

## Task 7: GC（保留集、宽限期、dry-run、统计）
- **Status**: `completed`
- **Priority**: high
- **Depends On**: Task 6
- **Completion Evidence**:
  - TR-7.1（rule）：9 blob 场景——dry-run 零删除；--yes 后唯一无引用 orphan 删除；valid 引用、仅 manifest 引用（记录已删/失效）的 blob 全部保留；orphan thumb 删、retained thumb 留。
  - TR-7.2（rule）：invalid 记录四态——超 grace 无 pin 删除；未超 grace 保留；有 pin 保留；valid 不动；二次 GC plan 为空。
  - TR-7.3（rule）：scanned=9/retained=8、DeletedBytes=999、DeletedRecords=1 与实际一致；plan/report JSON 可序列化；plan 后新增的 manifest pin 在 Execute 时通过 fresh-plan 交集生效，stale plan 不会误删（AC-13 强保证）。`-race` 通过。
- **Description**:
  - `internal/mediacache/pin.go`：`Pin(input)` 由发布成功的 derived keys + 记录构造 Manifest（manifest_id 由 account_key 与排序后的 derived keys 确定性生成），写 manifests/，把 manifest_id 回填对应 MediaRecord.manifest_pins 与引用方；相同 items 再发布返回既有 manifest，不新增文件、不重复回填。
- **Acceptance Criteria Addressed**: AC-11
- **Test Requirements**:
  - `rule` TR-6.1: 首次 Pin：manifest 文件存在、account/items（derived_key、source/uploaded digest、media_id、url）字段齐全、记录 manifest_pins 含该 id；以同 items（顺序打乱）再 Pin：manifests 文件数不变、返回同一 id；证据：pin 单测。

## Task 8: image.Processor 三路径接入缓存层
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 5
- **Description**:
  - 为 Processor 增加可选 mediacache 依赖（Option 注入；nil 时保持现有直传行为）。
  - UploadLocalImage/DownloadAndUpload/GenerateAndUpload：在压缩处理**前**对原始字节 ComputeIdentity（digest/phash），ProcessSpec 用 ForContent(cfg)；压缩后把 processedPath 交给 mediacache.Service.Resolve（Uploader 即现有 p.upload），替换原先直接 upload；缩略图由 Store 落盘。
  - 缓存初始化/读写失败：记录警告并回退现有直传路径（fail-open）。
- **Acceptance Criteria Addressed**: AC-15、AC-16（local/remote/ai）、AC-17（降级）
- **Test Requirements**:
  - `rule` TR-8.1: 三路径均断言身份取自压缩前原始字节（用「压缩必然触发」的大图 fixture 对比记录 source digest 与原始字节 digest）；证据：Processor 单测。
  - `rule` TR-8.2: 每路径第二次发布命中缓存（底层 upload 计数 0）；首次上传计数 1；证据：三路径缓存单测。
  - `rule` TR-8.3: Store 目录不可写时 Resolve 返回错误，Processor 回退直传成功且结果 media_id 正确；nil cache 时行为与当前完全一致；证据：降级单测。

## Task 9: 封面上传接入缓存层
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 8
- **Description**:
  - cmd 层 `uploadCoverImage` 改为经 mediacache Resolve（ProcessSpec 用 ForCover，pipeline_kind=cover），复用与内容图同一缓存根但独立 key；封面原图同样落 identity/thumb；保持函数签名与 --cover-media-id 旁路不变。
- **Acceptance Criteria Addressed**: AC-16（cover）、AC-4（cover 与 content 不串键）
- **Test Requirements**:
  - `rule` TR-9.1: 同一文件分别作为内容图与封面：产生两个 derived key、两次上传；封面第二次发布命中 cover 记录（upload=0）；证据：cmd 层封面链路测试。

## Task 10: 被动失效与发布流程有界重试
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 8、Task 9
- **Description**:
  - 在发布装配层捕获 CreateDraft 返回的失效类错误（IsInvalidMediaError/errcode）：把本次涉及的同账号记录批量置 invalid（含 reason=remote_error+code），随后触发**一次**资产重处理 + 草稿重建（复用现有 Service.Convert 或其拆分步骤），重试成功即返回；再失败则按原错误返回，不做第二次重试。
- **Acceptance Criteria Addressed**: AC-8
- **Test Requirements**:
  - `rule` TR-10.1: 注入首轮 CreateDraft 返回 40007、次轮成功：断言涉及记录最终 invalid、重传发生、草稿成功，且 CreateDraft 恰好 2 次；连续两轮都失败时不触发第三次；证据：发布流程重试单测（错误注入）。

## Task 11: `md2wechat media` 命令组
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 7
- **Description**:
  - `cmd/md2wechat/media.go`：
    - `media list [--account X] [--json]`
    - `media show <key-prefix|media-id> [--json]`
    - `media validate [--all | <key>...]`（经 Prober 强制探测并刷新状态）
    - `media gc [--dry-run] [--yes] [--json]`（默认 dry-run）
    - `media near-dupes [--file <path>] [--all-accounts] [--threshold N] [--json]`
  - 在 main.go 注册子命令；JSON envelope、错误退出码沿用现有命令约定；只读命令（list/show/near-dupes/gc dry-run）无网络与写入副作用。
- **Acceptance Criteria Addressed**: AC-14、AC-15（rubric 端到端）
- **Test Requirements**:
  - `rule` TR-11.1: CLI 契约矩阵——各命令成功路径 envelope 字段稳定、非法参数/未知 key 退出码非零、只读命令执行后缓存目录无文件变化（快照对比）、validate 与 gc --yes 的状态变化符合预期；证据：`cmd/md2wechat` media 契约测试。
  - `rubric` TR-11.2: 近重复端到端质量；scale 1-5；anchors 1=阻断发布或大量误报噪音，3=能提示但存在漏/误报或干扰主流程，5=缩放/轻压/小裁剪样本稳定提示、不同图安静、纯 advisory 不影响退出码与发布；threshold >= 4；证据：样本集上 near-dupes 与上传日志输出评估。

## Task 12: 文档与 SKILL 同步
- **Status**: `pending`
- **Priority**: medium
- **Depends On**: Task 11
- **Description**:
  - 按 Documentation Discipline 同步：README 入口、docs/CONFIG.md（cache.* 三层命名）、docs/DISCOVERY.md、docs/FAQ.md、docs/USAGE.md（media 命令）、skills/md2wechat/SKILL.md、platforms/openclaw/md2wechat/SKILL.md（仅写帮助 Agent 选对命令、避免危险副作用的必要内容，不写实现细节）。
  - 明确「近重复仅建议、GC 默认 dry-run、缓存默认启用不新增凭证」。
- **Acceptance Criteria Addressed**: AC-18（文档侧）
- **Test Requirements**:
  - `rule` TR-12.1: 上述文档均包含与当前 CLI 一致的命令/字段（以 `media --help` 与配置结构为准交叉核对），无 aspirational 表述；两处 SKILL.md 无实现路径/营销内容；证据：文档对照检查与 diff。

## Task 13: 全量门禁与发布前验证
- **Status**: `pending`
- **Priority**: high
- **Depends On**: Task 12
- **Description**:
  - 依次执行 `gofmt -l .`、`go vet ./...`、`GOCACHE=/tmp/md2wechat-go-build go test ./...`（含 -race 针对 mediacache）、`make quality-gates`；按 AGENTS.md 记录本地 API/layout smoke 是否适用（本次不触碰 layout 渲染；若本地 API 可用则对发布链路做一次真实上传 E2E，不可用则记录 skip 原因）。
- **Acceptance Criteria Addressed**: AC-18、AC-17（最终兼容确认）
- **Test Requirements**:
  - `rubric` TR-13.1: 工程完成度；scale 1-5；anchors 1=门禁失败或文档缺失，3=代码测试通过但门禁/文档不齐，5=gofmt/vet/full test/quality-gates 全绿且文档已同步、smoke 结果（或 skip 原因）有记录；threshold >= 4；证据：门禁与测试输出、smoke 记录。
