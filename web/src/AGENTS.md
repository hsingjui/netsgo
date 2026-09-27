# web/src 模块指南

## 模块边界

React SPA 前端，与 Server 同二进制发布（go:embed）。路由使用 TanStack Router Hash 模式。
所有 API 请求统一走 `lib/api.ts`；服务端状态由 TanStack Query 管理；全局 UI 状态用 Zustand。

## 目录分层

| 目录 | 职责 | 规则 |
|---|---|---|
| `components/ui/` | shadcn/ui 源码层 | 禁止手动修改和新建 |
| `components/custom/` | 业务组件 | 新增业务 UI 优先放这里 |
| `hooks/` | 查询、SSE、状态 hooks | 服务端数据不要复制为平行状态 |
| `lib/` | API 封装、路由、工具函数 | 不要散写裸 fetch |
| `routes/` | TanStack Router 页面 | dashboard/ 和 admin/ 两个分区 |
| `stores/` | Zustand 全局状态 | 仅放 UI 状态，不放服务端数据 |
| `i18n/` | 国际化资源 | 文案变更同步 locales/ 下所有语言 |
| `types/` | 共享 TS 类型 | 与 `pkg/protocol/` 语义对齐 |

## 高风险路径

- `lib/api.ts`：全局请求入口；改动影响所有 API 调用和鉴权头注入。
- `lib/router.ts` + `lib/auth.ts`：路由守卫和鉴权跳转；改错会锁死面板。
- `hooks/use-sse.ts`：SSE 事件流；断线重连逻辑影响实时状态一致性。
- `components/custom/dashboard/topology/`：拓扑图渲染，涉及 SVG 交互和客户端状态聚合。

## 局部验证

```bash
cd web && bun run build        # 类型检查 + 生产构建
cd web && bun run lint         # ESLint
cd web && bun test             # 单元测试（bun:test）
```

涉及嵌入资源或构建链路时需在仓库根目录跑 `make build`。
