# Report Service V2 — 产品文档（Review 稿）

> V2 目标：在不动 V1 页面/接口的前提下，重构 report-service 的**权限体系**与**上 Key 流程**，让工作室运营与主管理员的职责边界更清晰、Key 归属可审计、每条 Key 都能追溯到唯一的 remote newapi。
>
> **本篇是产品设计稿**，需要 review 通过后才进入实现阶段。

---

## 0. 关键决策速览

| 决策点 | 选择 | 说明 |
| --- | --- | --- |
| 部署形态 | 同一 report-service 进程 + `/api/v2/*` + 独立前端子应用 `/v2/` | V1 页面和接口保持不动，V2 复用同一份 `rs_auth_user`、`channels`、`remote_newapi_*` 数据 |
| RBAC 模型 | 预定义 action + 预定义 scope 组合的权限点 | 例：`keys.upload@own_studio`。admin 可以在 UI 里创建自定义角色，勾选权限点 |
| Key 分类 | 在 pool key 表新增 `key_type` 枚举字段（`regular` / `trial_5usd`） | 不同类型可以有独立的定价、上号策略、报表口径 |
| 上 Key 流程 | 双模式：**只入池**（等 admin 分配）或**直接指定 newapi**（现有行为） | studio operator 提交时二选一；池中未分配的 key 不激活、不产生用量 |
| Key → newapi 归属 | 全局唯一：一条 key hash 同一时间只能挂在一个 remote newapi 上 | 由数据库唯一约束 + 应用层前置校验共同保证 |
| 用量归属 | 沿用 `remote_channel_current.used_quota` + snapshot 时间序列 | 补充"studio 维度"聚合视图给 user 角色看 |
| Key 明文可见性 | **除"dead key"外，任何 API、导出、日志、错误信息一律不得回显 key 明文**；只掩码显示 | dead key = 被上游/监控自动禁用的 key（对应 remote channel `status=3`）。见 §3.6 全局硬约束 |

---

## 1. 与 V1 的关系

### 1.1 保留

- V1 的所有页面（AllKeys、RemoteChannels、RemoteChannelsStudio、KeyCapacity、KeyTester、ProviderTesting、Report、CacheReport、Profit、Users、Login）**继续运行**，路由继续挂在原路径。
- V1 的所有 `/api/*` 接口继续存在，V1 前端页面继续调用它们。
- V1 的角色枚举（1/2/5/10/100）继续在 JWT claim 里作为**兼容字段**存在，但 V2 只以 RBAC 计算得到的权限点为准。

### 1.2 新增

- V2 后端接口全部挂在 `/api/v2/*` 前缀下。
- V2 前端是一个独立的 SPA bundle（独立打包，独立入口），挂在 `/v2/` 路径。
- 新增 RBAC 相关的数据表（`rs_role`、`rs_role_permission`、`rs_user_role`），不动 `rs_auth_user`。
- 新增 Key Pool V2 相关的字段/表（`rs_key_pool` 或扩展 `local_pending_key` / `remote_pending_key`，详见 §5）。

### 1.3 共存约束

- V1 页面里 studio operator 直接上到 remote newapi 的行为**保留**（走原接口 `/api/remote-newapi/pending`）。
- V2 上到 pool 的 key 一旦被 admin 分配到 remote newapi，就走**同一条**上号通道（remote pending scheduler），只是入口不同。
- 一个 key hash 在 V1 上传后，V2 池里如果发现重复要显式提示并拒绝。

---

## 2. RBAC 权限模型

### 2.1 设计目标

- Admin（不含 superadmin）可以在页面上**新建角色、编辑角色、给用户分配角色**。
- 角色由若干**权限点**组成，权限点 = `action` + `scope`。
- Admin 只能授出自己拥有的权限（不能通过创建角色的方式给下属更高权限）。
- 系统内置的 4 个角色（superadmin / admin / studio_operator / user）不可删除，但除 superadmin 外可编辑权限点（也可以选择"锁 superadmin 和 admin，只允许新建自定义角色"，见 §2.6 开放问题）。

### 2.2 Action 目录（预定义、代码固化）

按业务模块分组，每个 action 表示一个原子的能力。命名统一小写点分。

| 分组 | Action | 说明 |
| --- | --- | --- |
| **keys.pool** | `keys.pool.upload` | 上传 key 到 key pool（不含直接上到 newapi 的能力） |
| | `keys.pool.assign` | 把 pool 里的 key 分配到某个 remote newapi |
| | `keys.pool.view` | 查看 pool 队列 |
| | `keys.pool.delete` | 删除 pool 中未分配的 key |
| **keys.newapi** | `keys.newapi.upload_direct` | 上传 key 时直接指定 remote newapi（跳过 pool 等待环节） |
| | `keys.newapi.view` | 查看已上线到 newapi 的 key 列表（**始终掩码**） |
| | `keys.newapi.rebind` | 把已上线的 key 从一个 newapi 迁到另一个（下线 + 重新上号） |
| | `keys.newapi.disable` | 停用/删除已上线的 key |
| | `keys.reveal_dead` | 查看 dead key 的完整明文（用于轮换/排查，见 §3.6） |
| **keys.pricing** | `keys.pricing.set` | 设置 key 的上游 unit_price_cny / quota_usd |
| **usage** | `usage.view` | 查看 key 用量 |
| **reports** | `reports.view` | 查看日报/毛利报表 |
| | `reports.export` | 导出 CSV/HTML |
| **remote_newapi** | `remote_newapi.profile.manage` | 增删改 remote newapi profile（含 host / access_token） |
| | `remote_newapi.policy.manage` | 配置 studio ↔ newapi 的接受策略 |
| **users** | `users.view` | 查看用户列表 |
| | `users.create` | 创建新用户 |
| | `users.disable` | 停用用户 |
| | `users.reset_password` | 重置用户密码 |
| | `users.assign_role` | 给用户分配角色 |
| **roles** | `roles.view` | 查看角色列表 |
| | `roles.manage` | 创建/编辑/删除自定义角色 |
| **testing** | `testing.key_tester` | 使用 Key Tester 页面 |
| | `testing.provider_testing` | 使用 Provider Testing 页面 |
| **system** | `system.config` | 系统全局配置（pool interval、通知策略等） |

新增业务模块时，往这份目录里加 action，不允许在数据库里凭空写一个未登记的 action 字符串（应用启动时对不认识的 action 打日志 + 忽略）。

### 2.3 Scope 目录（预定义、代码固化）

Scope 描述"这个 action 作用在哪些数据上"。

| Scope | 语义 |
| --- | --- |
| `global` | 全站，无过滤 |
| `own_studio` | 只作用在"当前用户绑定的 studio"对应的数据 |
| `any_studio` | 任意 studio（等价于 global，但语义上强调"跨 studio"） |
| `self` | 只作用在"当前用户自己"（例如 `users.reset_password@self` = 只能改自己的密码） |

Scope 是**枚举**，不接受自由字符串。一个权限点是 `action@scope` 的组合，例如：

- `keys.pool.upload@own_studio` — 上传 key 到自己 studio 的池
- `usage.view@own_studio` — 只能看自己 studio 的用量
- `usage.view@any_studio` — 可以看所有 studio 的用量
- `users.create@global` — 可以创建任何用户

### 2.4 内置角色的默认权限映射

内置角色启动时通过 seed 自动写入 `rs_role_permission` 表；后续 admin 可以编辑 admin / studio_operator / user 的权限（superadmin 是硬编码超级权限，不写入表也不受表约束）。

| 角色 | 权限点（默认） |
| --- | --- |
| **superadmin** | **全部**（硬编码短路，不进 RBAC 表查询），含 `keys.reveal_dead@global` |
| **admin** | 除以下之外的全部权限：`remote_newapi.profile.manage`、`users.create`（限"创建 admin/superadmin"）、`users.assign_role`（限"授出 admin/superadmin 角色"）、**`keys.reveal_dead`（默认不给，需 superadmin 显式授出）**——具体见 §2.5 越权保护 |
| **studio_operator** | `keys.pool.upload@own_studio`、`keys.pool.view@own_studio`、`keys.pool.delete@own_studio`、`keys.newapi.view@own_studio`、`usage.view@own_studio`、`testing.key_tester` |
| **user** | `keys.newapi.view@own_studio`、`usage.view@own_studio`、`reports.view@own_studio` |

**注意**：admin 的原始需求是"不能添加 newapi、不能添加 superadmin 用户、不能改 superadmin"。这三条通过下列权限点表达：

- "不能添加 newapi" = admin 不持有 `remote_newapi.profile.manage`
- "不能改 superadmin" = 见 §2.5 用户操作的角色等级校验
- "不能添加 superadmin 用户" = 见 §2.5 角色分配的向下约束

### 2.5 越权保护（Role Ladder）

每个角色有一个 `level`（整数，可编辑，值越大权限越高）。默认：superadmin=100，admin=50，studio_operator=20，user=10，自定义角色由创建者设定但**不能超过创建者自己的 level**。

**用户操作规则**：

- 一个用户可以看到 / 修改 / 停用的目标用户，必须满足 `target.max_role_level < caller.max_role_level`。
- superadmin 短路允许操作所有人，但保留"不能停用/删除最后一个 superadmin"的红线（沿用 V1 现有实现）。

**角色分配规则**：

- 一个用户能授出的角色，其 `level` 必须严格小于自己的 `max_role_level`。
- 一个用户能创建/编辑的自定义角色，其 `level` 上限也是 `caller.max_role_level - 1`；权限点必须是 `caller` 自己拥有的子集。

这条约束的**具体推论**（回应你的原始需求）：

- admin(level=50) 不能创建 level>=50 的角色 → 不能造出一个能"改 superadmin"的角色
- admin 不能持有 `remote_newapi.profile.manage` → 也不能授出它 → 派生的自定义角色都不能加 newapi
- admin 不能持有"创建 superadmin"的能力 → 不能把用户升成 superadmin

### 2.6 页面：角色管理（新页面）

新页面 `/v2/roles`，需要 `roles.view` 或 `roles.manage`：

- **角色列表**：显示所有角色，每行展示角色名、level、权限点数量、绑定用户数、是否为内置。
- **新建/编辑角色抽屉**：左侧填角色名、level；右侧按 §2.2 的模块分组显示权限点，checkbox 勾选。灰掉当前用户没有的权限点。
- **删除角色**：仅允许删除自定义角色，且该角色下用户数为 0。

### 2.7 页面：用户 & 角色分配（新页面）

新页面 `/v2/users`，取代 V1 `Users.tsx` 的位置但只在 V2 前端里生效：

- 除现有的"创建用户、改密、停用、启用、删除"外，新增**角色分配抽屉**：勾选一个或多个角色（一个用户可以有多个角色，最终权限 = 各角色权限的并集，取 level 最大值作为 max_role_level）。
- 分配时按 §2.5 灰掉不能授出的角色。
- 用户列表里显示"绑定的 studio"字段，仍复用 `rs_auth_user.studio`。

### 2.8 开放问题

- **Q1**：admin 可以编辑内置的 admin / studio_operator / user 三个角色吗？还是这三个角色的权限点也固化在代码里、只有自定义角色可编辑？
  - 推荐：**内置角色的权限点在代码里 seed，允许 superadmin 编辑，admin 不能编辑内置角色**。这样避免 admin 通过"给 admin 角色加权限点"绕过限制。
- **Q2**：一个用户可以同时有多个角色吗？还是只能有一个？
  - 推荐：多角色，取权限并集。这样兼容"一个用户既是 studio_operator 又是 tester"的场景。
- **Q3**：`user` 和 `studio_operator` 都绑定 studio，但 `user` 不允许上 key。V1 里 `role=1` 就是 user，这个含义在 V2 保留。

---

## 3. Key Pool V2

### 3.1 Key 分类：`key_type` 字段

在 pool key 表（`rs_key_pool`，见 §5）新增枚举字段 `key_type`：

| 值 | 语义 | 默认 quota_usd | 上号策略 |
| --- | --- | --- | --- |
| `regular` | 普通 key | 由上传者填写（可为空表示不限额） | 常规 pool 调度 |
| `trial_5usd` | 5 刀试用 key | 固定 5.00 | 常规 pool 调度，但报表口径独立聚合，用于试用统计 |

- `key_type` 由 studio operator 在上传表单里显式选择，不做智能推断。
- 报表 / 用量视图新增"按类型过滤"的选项。
- 未来要加新类型（例如 `bulk_10usd`）时，只需要往枚举里加值 + seed 默认 quota。

### 3.2 上 Key 流程 V2

studio operator 在 `/v2/keys/upload` 页面提交一批 key，表单字段：

- `studio`：**锁定为 JWT 里的 studio**，不允许改（沿用 V1 行为）
- `key_type`：`regular` / `trial_5usd`（radio）
- `target_mode`：`pool_only` / `direct_newapi`（radio）
  - `pool_only`：只入池，等 admin 分配；此时不需要选 newapi
  - `direct_newapi`：入池同时指定一个 remote newapi profile（这就是当前 V1 的行为，V2 里作为二选一之一保留）
- `target_profile_id`：仅 `target_mode=direct_newapi` 时可见
- `models`、`group`、`name_prefix`、`quota_usd`：按类型给出合理默认
- key 列表：每行是明文 key

**后端处理**（`POST /api/v2/keys/pool`）：

1. 校验 studio operator 有 `keys.pool.upload@own_studio` 权限。
2. 校验批次里每条 key 的 hash 在 `rs_key_pool` 里没有 `active` 状态的记录（跨 profile / studio 全局唯一，见 §3.4）。
3. 写入 `rs_key_pool`，状态：
   - `target_mode=pool_only` → 状态 `awaiting_assignment`（等待 admin 分配）
   - `target_mode=direct_newapi` → 状态 `pending`（进入现有的 remote pending scheduler 逻辑），同时在 `rs_key_pool` 里记下 `assigned_profile_id`
4. `target_mode=direct_newapi` 还需要额外校验 studio ↔ profile 的 accepting_keys 策略（沿用 V1 `studioAccepting`）。

**关键点**：`awaiting_assignment` 状态的 key **不激活、不上号、不产生任何用量**。它就是一个待分配的凭证。

### 3.3 Admin 分配 Key 到 newapi

Admin 在 `/v2/keys/pool` 页面看到所有 `awaiting_assignment` 的 key，可以：

- **单条分配**：选一条 key + 一个 profile → 状态转 `pending`，进入 remote pending scheduler。
- **批量分配**：多选 key + 一个 profile → 批量转 `pending`。
- **按规则分配**（可选，V2.1 迭代再做）：按 studio、按 key_type 设置默认目标 profile，pool key 到达时自动分配。

**分配接口**：`POST /api/v2/keys/pool/assign`，body：`{ key_ids: [...], profile_id: N }`。需要 `keys.pool.assign` 权限。

分配后：

- `rs_key_pool.status` = `pending`
- `rs_key_pool.assigned_profile_id` = 目标 profile
- 生成对应的 `remote_pending_key` 行（`profile_id`, `key_encrypted`, `key_hash`, `tag=studio`, ...），交给现有 scheduler 上号。
- 上号成功后：`rs_key_pool.status` = `active`、`rs_key_pool.remote_channel_id` 记录目标 channel id。

### 3.4 "一条 Key 只能给一个 newapi" 约束

**数据库层**：在 `rs_key_pool` 表上加一个**部分唯一索引**：

```sql
CREATE UNIQUE INDEX ux_key_pool_active_hash
  ON rs_key_pool (key_hash)
  WHERE status IN ('awaiting_assignment', 'pending', 'active');
```

（SQLite / PostgreSQL 都支持部分索引；MySQL 走"生成列 + 唯一索引"或应用层加锁 + 事务校验的兼容路径。三库兼容策略见 §5.3。）

**应用层**：`POST /api/v2/keys/pool` 入池前用 SELECT + INSERT 事务确认；`POST /api/v2/keys/pool/assign` 分配前再次确认目标 key 不在其他 profile 的活动记录里。

**下线 / 迁移**：如果要把一条已 `active` 的 key 迁到别的 profile，走 `keys.newapi.rebind`：

1. 在目标 profile 下先 dry-run 校验策略
2. 从当前 profile 删除 remote channel（走 remote newapi API）
3. `rs_key_pool.status` 保持不变，`assigned_profile_id` 改成新的
4. 生成新的 `remote_pending_key` 交给 scheduler

迁移期间 key 的用量归属：**以 remote channel 的实际生命周期为准**，历史用量留在旧 channel 上，新用量归到新 channel（不合并）。这样账目最清楚。

### 3.5 Key 用量统计

- studio_operator 在 `/v2/usage/my` 看到"自己上传的 key"的用量：按 `rs_key_pool.uploaded_by = self` 过滤。
- user 在 `/v2/usage/studio` 看到"绑定 studio 的所有 key"的用量：按 `rs_key_pool.studio = self.studio` 过滤。
- admin/superadmin 在 `/v2/usage/all` 看全站，可按 studio / profile / key_type 切片。

**数据来源**：

- `remote_channel_current.used_quota`（当前累计）
- `remote_channel_snapshot`（15 分钟粒度的时间序列，画折线）
- V2 加一层聚合：`SELECT SUM(used_quota) FROM remote_channel_current WHERE (profile_id, remote_channel_id) IN (SELECT ... FROM rs_key_pool WHERE studio = ?)`

不重建用量表，只加聚合视图。

### 3.6 Key 明文可见性策略（全局硬约束）

这条规则**优先于**其他任何设计。任何新增的 endpoint、页面、导出、日志、审计都必须遵守；实现时以 code review 一条一条卡住。

#### 3.6.1 "Dead key" 的定义

**dead key = 被上游/监控自动禁用的 key**，具体判定：

- 对应的 `remote_channel_current.status = 3`（remote newapi 上游把这条 channel 自动禁用），**或者**
- 对应的本地 `channels.status = 3`（V1 通道的 auto-disabled 状态，V2 沿用同一含义）

**只有这一种状态**才允许在授权范围内暴露完整明文。其他所有状态（含 `awaiting_assignment` / `pending` / `active` / `used` / `failed`）**一律掩码**。

- `failed`（上号重试耗尽）—— 明文仍然可能是有效的活 key，不能暴露
- `used`（人工/流程主动下线）—— 明文仍然可能是有效的活 key，不能暴露
- `active` —— 生产中，明文绝对不能出现在任何响应里

#### 3.6.2 掩码格式

沿用 V1 约定：`…` + key 的最后 8 位字符。例：`sk-abcd…4x9zP7Qm`。少于 8 位的 key 直接返回空字符串（不应该出现，但做防御）。

响应字段命名统一：`key_masked`（掩码后字符串）与 `key`（明文，仅 dead + 授权时才出现，否则不出现在 JSON 中）。前端只信任 `key`，不存在 `key` 字段就当没有。

#### 3.6.3 授权点：`keys.reveal_dead`

新增权限点 `keys.reveal_dead`（见 §2.2）。持有此权限的用户，在 GET 类接口和 CSV/HTML 导出里，dead key 的响应会额外带上 `key` 字段（明文）。未持有此权限的用户，即使是 dead key 也只看到 `key_masked`。

默认分配（见 §2.4 更新版）：

- **superadmin**：默认持有 `keys.reveal_dead@global`
- **admin**：默认**不持有**（需要 superadmin 显式在角色管理页面里加进去）—— 让 admin 主动申请，可审计
- **studio_operator / user**：默认不持有；且由于 admin 也不持有，operator 无法通过自定义角色被授出

#### 3.6.4 服务端存储与不落地字段

- `rs_key_pool.key_encrypted`：**永远不出现在任何 API 响应里**。GORM model 上加 `json:"-"` 标签（或 struct DTO 剥离），review 时一票否决。
- `rs_key_pool.key_hash`：**默认不出现在响应里**。虽然 SHA-256 preimage 不可逆，但没有客户端合法用途，避免为将来的攻击链路铺路。
- `remote_pending_key.key_encrypted`：同上（V1 表，V2 复用时也遵循此规则）。

#### 3.6.5 `failed_reason` 清洗规则

`rs_key_pool.failed_reason` 记录上号失败原因，而上游 API 的错误信息**可能回显原始请求的 key 前缀**。落表前必须过一次清洗：

1. 用正则 `sk-[A-Za-z0-9\-_]{20,}`、`Bearer\s+\S{20,}`、以及项目内已知的 key 前缀（如 `AKIA[0-9A-Z]{16}`、`AIza[0-9A-Za-z\-_]{35}` 等）做替换，命中即整体替换为 `[REDACTED]`。
2. 长度 > 512 的错误信息截断到 512。
3. 清洗后的字符串才允许写入。

同规则应用于 `log.Printf` 时输出错误：使用统一的 `sanitizeForLog(err)` 包装，禁止直接 `log.Printf("%v", err)` 输出可能包含 key 的上游错误。

#### 3.6.6 导出（CSV/HTML）继承同一规则

`reports.export` 权限允许导出，但导出行的 key 字段遵循 §3.6.1 / §3.6.3：

- Dead key 且导出方持有 `keys.reveal_dead` → 明文列
- 其他 → 掩码列

**这就是当前 V1 `handleExportCSV` 在 commit 68b2f7b1 后的行为的 V2 迁移目标**。

#### 3.6.7 自查清单（实现时逐条对拍）

| # | 检查项 | 通过标准 |
| --- | --- | --- |
| 1 | 后端 DTO 上 `key_encrypted`、`key_hash` 字段 | 均标 `json:"-"` 或不在 DTO 里 |
| 2 | `GET /api/v2/keys/pool` 响应 | 只含 `key_masked`；dead + 有权限时才含 `key` |
| 3 | `GET /api/v2/keys/active` 响应 | 同上 |
| 4 | `GET /api/v2/usage` 响应 | 只含 `key_masked`（若返回按 key 的明细） |
| 5 | `POST /api/v2/keys/pool` 响应 | 只返回 id 列表 + 状态；不回显任何 key 字段 |
| 6 | `POST /api/v2/keys/pool/assign` / `rebind` / `disable` 响应 | 只返回受影响行的 id 与新状态 |
| 7 | `failed_reason` 落表 | 走 `sanitizeUpstreamMessage`，测试用例覆盖上游 echo key 场景 |
| 8 | 服务端日志 | 全站 grep `log.Printf` / `fmt.Errorf` 不出现 `p.Key` / `payload.Key` 拼接 |
| 9 | 导出（CSV/HTML）行 | 与 GET 接口用同一份序列化函数，dead-only reveal 逻辑集中在一处 |
| 10 | 错误响应体 | 不含 key 明文（例："key already exists" 错误里禁止拼接明文） |

---

## 4. V2 API 端点一览

所有 V2 endpoint 挂在 `/api/v2/*`，鉴权中间件先跑 V1 的 JWT / SSO 逻辑取到 user_id，再查 RBAC 权限点。

### 4.1 RBAC

| Method | Path | 权限点 |
| --- | --- | --- |
| GET | `/api/v2/roles` | `roles.view` |
| POST | `/api/v2/roles` | `roles.manage` |
| PATCH | `/api/v2/roles/:id` | `roles.manage` |
| DELETE | `/api/v2/roles/:id` | `roles.manage`（且角色非内置、绑定用户数为 0） |
| GET | `/api/v2/permissions` | `roles.view`（返回 action 目录 + scope 目录） |
| GET | `/api/v2/users` | `users.view` |
| POST | `/api/v2/users` | `users.create` |
| PATCH | `/api/v2/users/:id` | 见 §2.5 校验 |
| POST | `/api/v2/users/:id/roles` | `users.assign_role` |
| DELETE | `/api/v2/users/:id/roles/:role_id` | `users.assign_role` |

### 4.2 Key Pool

响应中 key 字段格式统一遵守 §3.6：`key_masked` 恒返回；`key`（明文）仅当行是 dead 且调用者持有 `keys.reveal_dead@<scope>` 时才出现。

| Method | Path | 权限点 | 响应含 key 字段 |
| --- | --- | --- | --- |
| POST | `/api/v2/keys/pool` | `keys.pool.upload@own_studio` 或 `keys.newapi.upload_direct` | ❌ 只返回 `{id, status}` 列表，不回显 key |
| GET | `/api/v2/keys/pool` | `keys.pool.view`（按 scope 过滤） | `key_masked`；dead + `keys.reveal_dead` 时含 `key` |
| GET | `/api/v2/keys/active` | `keys.newapi.view`（按 scope 过滤） | 同上 |
| DELETE | `/api/v2/keys/pool/:id` | `keys.pool.delete`（仅 `awaiting_assignment`） | ❌ 只返回 `{ok: true}` |
| POST | `/api/v2/keys/pool/assign` | `keys.pool.assign` | ❌ 只返回受影响 id 列表与新状态 |
| POST | `/api/v2/keys/rebind` | `keys.newapi.rebind` | ❌ 只返回受影响 id 列表与新状态 |
| POST | `/api/v2/keys/disable` | `keys.newapi.disable` | ❌ 只返回受影响 id 列表与新状态 |
| GET | `/api/v2/usage` | `usage.view`（按 scope 过滤） | 若返回 per-key 明细，遵守同一规则；纯聚合无 key 字段 |
| GET | `/api/v2/keys/export.csv` | `reports.export` + `keys.pool.view` 或 `keys.newapi.view` | 掩码列；dead + `keys.reveal_dead` 时替换为明文列。**入口只有一个 CSV writer，不允许各页面自行拼装** |

### 4.3 元数据

| Method | Path | 权限点 | 敏感字段处理 |
| --- | --- | --- | --- |
| GET | `/api/v2/profiles` | `remote_newapi.profile.manage`（完整）/ 任意登录（slim：仅 id + name + default_models） | `access_token_enc` **任何角色都不返回**；写入走 PATCH，读取时只返回是否已设置的 boolean `has_access_token` |
| POST/PATCH | `/api/v2/profiles` | `remote_newapi.profile.manage` | `access_token` 只能写，从不读回 |
| GET | `/api/v2/studios` | 任意登录用户 | — |
| GET | `/api/v2/me` | 任意登录用户，返回当前用户 + 权限点集合 | — |

---

## 5. 数据模型变更

### 5.1 新增表：`rs_role`

```sql
CREATE TABLE rs_role (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  level        INT  NOT NULL,
  is_builtin   BOOL NOT NULL DEFAULT false,
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
```

### 5.2 新增表：`rs_role_permission`

```sql
CREATE TABLE rs_role_permission (
  role_id BIGINT NOT NULL,
  action  TEXT   NOT NULL,
  scope   TEXT   NOT NULL,
  PRIMARY KEY (role_id, action, scope)
);
```

### 5.3 新增表：`rs_user_role`

```sql
CREATE TABLE rs_user_role (
  user_id BIGINT NOT NULL,
  role_id BIGINT NOT NULL,
  PRIMARY KEY (user_id, role_id)
);
```

### 5.4 新增表：`rs_key_pool`

```sql
CREATE TABLE rs_key_pool (
  id                  BIGSERIAL PRIMARY KEY,
  studio              TEXT   NOT NULL,
  uploaded_by         BIGINT NOT NULL,        -- rs_auth_user.id
  key_type            TEXT   NOT NULL,        -- 'regular' | 'trial_5usd'
  key_hash            TEXT   NOT NULL,        -- 服务端内部字段，DTO 上 json:"-"，不出 API
  key_encrypted       TEXT   NOT NULL,        -- 服务端内部字段，DTO 上 json:"-"，不出 API；仅在上号/迁移时解密
  key_last8           TEXT   NOT NULL,        -- 掩码显示用，明文最后 8 位；这是 API 响应里 key_masked 的数据源
  quota_usd           NUMERIC(12,4),
  models              TEXT,
  name_prefix         TEXT,
  group_name          TEXT,
  status              TEXT   NOT NULL,        -- 'awaiting_assignment' | 'pending' | 'active' | 'used' | 'failed'
  assigned_profile_id BIGINT,
  remote_channel_id   BIGINT,
  failed_reason       TEXT,                   -- 落表前必须走 sanitizeUpstreamMessage()，见 §3.6.5
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL
);

-- 唯一约束：同一 hash 同时只能有一条 "生效中" 的记录
-- PG / SQLite：部分索引
CREATE UNIQUE INDEX ux_key_pool_active_hash
  ON rs_key_pool (key_hash)
  WHERE status IN ('awaiting_assignment', 'pending', 'active');
-- MySQL：走应用层事务 + 普通索引 (key_hash, status)，插入前 SELECT 校验
CREATE INDEX ix_key_pool_hash_status ON rs_key_pool (key_hash, status);
```

按项目规范（CLAUDE.md §"Database compatibility"）：

- 用 GORM `Create` / `Where` / `Updates`
- 三库兼容分支通过 `common.UsingMainDatabase(...)` 分派
- Boolean 默认值走代码 normalize，不用 GORM `default:true`
- 时间列继续用 `BIGINT`（Unix 秒），与 V1 保持一致

### 5.5 与 V1 表的关系

- `rs_auth_user`：**不动**。JWT claim 里的 `role` 字段保留（V1 兼容），V2 鉴权额外查 `rs_user_role`。
- `local_pending_key` / `remote_pending_key`：**不动**。V2 分配 key 给 profile 时会往 `remote_pending_key` 插入一行，由现有 scheduler 消费。
- `channels`、`remote_channel_current`、`remote_channel_snapshot`：**不动**。V2 的用量查询直接 join 这些表。
- `report_key_quotas`：**不动**。V2 设置 quota / unit_price_cny 时继续写这张表。

### 5.6 迁移与 seed

启动时自动 migrate：

1. 建 4 张新表（如果不存在）
2. Seed 内置角色（4 个）+ 内置角色的默认权限点（按 §2.4）
3. 把现有 `rs_auth_user.role` 值映射到内置角色，写入 `rs_user_role`：
   - role=100 → superadmin
   - role=10 → admin
   - role=5 → 自定义 tester 角色（seed 一份带 `testing.*` 权限点的角色，命名 `tester`）
   - role=2 → studio_operator
   - role=1 → user
4. 迁移是幂等的（重复运行不重复插入）

---

## 6. 前端子应用

### 6.1 打包与路由

- 目录：`scripts/report-service/frontend-v2/`（与 `frontend/` 平级，V1 前端零改动）
- 构建产物挂在 `/v2/` 前缀下，由同一个 report-service Go 进程静态托管
- 路由：`/v2/login`、`/v2/keys/pool`、`/v2/keys/upload`、`/v2/users`、`/v2/roles`、`/v2/usage/{my,studio,all}`、`/v2/profiles`（仅 superadmin 可见）
- 技术栈可以延续 V1（React + Vite + Tailwind），但独立 `package.json` 和依赖树，方便未来独立升级

### 6.2 页面清单

| 页面 | 需要权限点 | 说明 |
| --- | --- | --- |
| `/v2/login` | 无 | 复用 V1 `/api/login` 或走 SSO |
| `/v2/keys/upload` | `keys.pool.upload` 或 `keys.newapi.upload_direct` | 上 key 表单（§3.2） |
| `/v2/keys/pool` | `keys.pool.view` | Pool 队列 + 分配按钮 |
| `/v2/keys/active` | `keys.newapi.view` | 已上线 key 列表 + rebind/disable 操作 |
| `/v2/usage/my` | `usage.view@self`（隐含） | 自己上传的 key 用量 |
| `/v2/usage/studio` | `usage.view@own_studio` | 本 studio 所有 key 用量 |
| `/v2/usage/all` | `usage.view@any_studio` | 全站切片 |
| `/v2/users` | `users.view` | 用户列表 + 角色分配 |
| `/v2/roles` | `roles.view` | 角色 / 权限点管理 |
| `/v2/profiles` | `remote_newapi.profile.manage` | remote newapi profile 增删改 |
| `/v2/settings` | `system.config` | pool interval、通知策略等 |

### 6.3 前端权限渲染

- 登录后拉一次 `/api/v2/me`，拿到当前用户的**权限点集合**（形如 `["keys.pool.upload@own_studio", "usage.view@own_studio"]`）。
- 路由守卫按权限点决定是否可访问某个 `/v2/*` 路由。
- 页面内的按钮、菜单项按权限点显式判断。
- **原则**：前端权限渲染是 UX，安全边界永远在后端。

---

## 7. 里程碑

拆成 3 期，每期都是可 review 可回滚的独立 PR：

- **M1（RBAC 基础）**：4 张 RBAC 表 + seed + `/api/v2/roles`、`/api/v2/users` 接口 + `/v2/roles`、`/v2/users` 两个页面。此时 V2 前端只有这两页，其他跳回 V1。
- **M2（Key Pool V2）**：`rs_key_pool` 表 + `/api/v2/keys/pool*` 接口 + `/v2/keys/upload`、`/v2/keys/pool`、`/v2/keys/active` 三个页面。这一期完成之后，工作室实际的上 key 流程可以切到 V2。
- **M3（用量视图 & 收尾）**：`/v2/usage/*` 三个页面 + `/v2/profiles` + `/v2/settings`。V1 页面进入维护状态（继续可用但不再新增功能）。

---

## 8. 开放问题（Review 时请重点看）

1. **内置角色 admin 能否编辑权限？** §2.6 Q1。推荐"只有 superadmin 能编辑内置角色的权限点"。
2. **一个用户能拥有多个角色吗？** §2.6 Q2。推荐允许，权限取并集。
3. **`key_type` 目前只有 `regular` 和 `trial_5usd` 两个值，将来加类型的流程？** 加代码常量 + seed 默认 quota + 加报表切片 = 一次发布搞定。
4. **`awaiting_assignment` 状态的 key 有没有 TTL？** 建议加一个"超过 N 天未分配自动删除"策略，防止 pool 无限膨胀。默认 N = 30，可配置。
5. **studio_operator 从 V2 上传时能不能同时指定 remote newapi？** §3.2 里的 `direct_newapi` 模式默认允许（沿用 V1 行为），但如果你希望 V2 里"上池"和"直接上号"分成两种权限点（`keys.pool.upload` vs `keys.newapi.upload_direct`），并且默认只给 studio_operator `keys.pool.upload`，需要在 §2.4 微调。
6. **RBAC 表用不用 casbin？** 项目 root 下已经有 `common/casbin_rule.go` 和 `model/casbin_rule.go`。本文档目前的设计是**手写一个轻量 RBAC**（表结构简单、query 直接），不引入 casbin。若你希望统一走 casbin，需要额外评估性能和 policy 表达。
7. **V1 页面何时下线？** 目前设计为**长期共存**，没有下线计划。如果确认要下线，需要单独规划迁移路径。

---

## 9. 附录：权限决策示例

**场景 A**：studio_operator "小张" 隶属 studio `alpha`，想上 5 张 5 刀试用 key。

1. 前端页面 `/v2/keys/upload` 从 `/api/v2/me` 拿到权限点 `["keys.pool.upload@own_studio", "keys.pool.view@own_studio", ...]`。
2. 小张选择 `key_type=trial_5usd`、`target_mode=pool_only`、填入 5 张明文 key、提交。
3. 后端 `POST /api/v2/keys/pool` 校验 `keys.pool.upload@own_studio`（通过），把 studio 强制设为 `alpha`（从 JWT），quota_usd 默认为 5，插入 5 行 `rs_key_pool`，状态 `awaiting_assignment`。
4. Admin "老王" 打开 `/v2/keys/pool`，看到 5 行 `awaiting_assignment` 的 key，选中并点"分配到 US Prod newapi"。
5. 后端 `POST /api/v2/keys/pool/assign` 校验 `keys.pool.assign`（通过），把 5 行状态改为 `pending`，同时往 `remote_pending_key` 插 5 行，scheduler 按 pool 策略上号。
6. 上号成功后 `rs_key_pool.status=active`、`remote_channel_id` 记录 US Prod 的 channel id。
7. 小张在 `/v2/usage/my` 看到 5 张 key 的用量，key 列显示为 `…4x9zP7Qm` 之类的掩码；小张不持有 `keys.reveal_dead`，即使某张 key 后来被上游自动禁用，前端也只显示掩码。
8. 若其中一张 key 被上游自动禁用（remote channel `status=3`），superadmin 老李在 `/v2/keys/pool` 的 dead 列表里点"查看明文"或导出 CSV 时，才拿得到那一张 key 的原文用于轮换。

**场景 B**：admin "老王" 想创建一个"studio manager"角色，权限比 studio_operator 多"看本 studio 所有报表"，但不能加 newapi。

1. 老王打开 `/v2/roles` → 新建角色 → 名字 `studio_manager`、level=25。
2. 勾选：`keys.pool.upload@own_studio`、`keys.pool.view@own_studio`、`keys.newapi.view@own_studio`、`usage.view@own_studio`、`reports.view@own_studio`、`reports.export@own_studio`。
3. `remote_newapi.profile.manage` 这一项在勾选面板里被灰掉（老王自己不持有）。
4. 提交后 `rs_role` + `rs_role_permission` 落库。
5. 老王打开 `/v2/users` 找到"小李"，分配 `studio_manager` 角色。小李下次登录时 `/api/v2/me` 就返回这些权限点。
